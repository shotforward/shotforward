#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

use serde::Deserialize;
use std::ffi::OsString;
use std::fs::{self, File, OpenOptions};
use std::io::Write;
use std::path::{Path, PathBuf};
use std::sync::{Arc, Mutex};
use std::time::{SystemTime, UNIX_EPOCH};
use tauri::{AppHandle, Manager, RunEvent, WebviewUrl, WebviewWindow, WebviewWindowBuilder};
use tauri_plugin_dialog::DialogExt;
use tauri_plugin_shell::ShellExt;
use tauri_plugin_shell::process::{CommandChild, CommandEvent};
use url::{Host, Url};

const MAX_ANNOUNCEMENT_BYTES: usize = 8 * 1024;
const MAX_DAEMON_LOG_BYTES: u64 = 5 * 1024 * 1024;
const SESSION_COOKIE_NAME: &str = "shotforward_session";

#[tauri::command]
async fn select_project_parent(app: AppHandle) -> Result<Option<String>, String> {
    app.dialog()
        .file()
        .set_title("Choose a folder for ShotForward projects")
        .blocking_pick_folder()
        .map(|selected| {
            selected
                .into_path()
                .map(|path| path.to_string_lossy().into_owned())
                .map_err(|_| "the selected folder is not a local filesystem path".to_owned())
        })
        .transpose()
}

#[tauri::command]
async fn select_script_file(app: AppHandle) -> Result<Option<String>, String> {
    app.dialog()
        .file()
        .set_title("Import a ShotForward script")
        .add_filter("Markdown and text", &["md", "txt"])
        .blocking_pick_file()
        .map(|selected| {
            selected
                .into_path()
                .map_err(|_| "the selected script is not a local filesystem path".to_owned())
                .and_then(script_file_path)
        })
        .transpose()
}

fn script_file_path(path: PathBuf) -> Result<String, String> {
    let supported = path
        .extension()
        .and_then(|extension| extension.to_str())
        .is_some_and(|extension| {
            extension.eq_ignore_ascii_case("md") || extension.eq_ignore_ascii_case("txt")
        });
    if !supported {
        return Err("only Markdown and plain-text scripts can be imported".to_owned());
    }
    Ok(path.to_string_lossy().into_owned())
}

#[tauri::command]
fn open_external(app: AppHandle, url: String) -> Result<(), String> {
    let parsed = validate_external_url(&url)?;
    #[allow(deprecated)]
    app.shell()
        .open(parsed.to_string(), None)
        .map_err(|_| "the system browser could not be opened".to_owned())
}

#[tauri::command]
async fn save_download(
    app: AppHandle,
    window: WebviewWindow,
    url: String,
    suggested_name: String,
) -> Result<bool, String> {
    let current = window
        .url()
        .map_err(|_| download_failure(&app, "validate", "download_context_unavailable", ""))?;
    let download_url = validate_download_url(&current, &url)
        .map_err(|_| download_failure(&app, "validate", "download_url_invalid", ""))?;
    let file_name = download_file_name(&suggested_name, "")
        .map_err(|_| download_failure(&app, "validate", "download_name_invalid", ""))?;
    let selected = app
        .dialog()
        .file()
        .set_title("Save ShotForward download")
        .set_file_name(file_name)
        .blocking_save_file();
    let Some(selected) = selected else {
        append_desktop_log(&app, "download", "cancelled", "dialog");
        return Ok(false);
    };
    let destination = selected
        .into_path()
        .map_err(|_| download_failure(&app, "destination", "download_destination_invalid", ""))?;
    let cookies = window
        .cookies()
        .map_err(|_| download_failure(&app, "session", "download_session_unavailable", "read"))?;
    // `cookies_for_url` in Wry performs an exact domain comparison on macOS and
    // can omit valid loopback cookies. This WebView only loads the validated
    // local origin, so read its store and forward only our named session cookie.
    let cookie_header =
        session_cookie_header(cookies.iter().map(|cookie| (cookie.name(), cookie.value())))
            .ok_or_else(|| {
                download_failure(&app, "session", "download_session_unavailable", "missing")
            })?;
    let client = reqwest::Client::builder()
        .no_proxy()
        .redirect(reqwest::redirect::Policy::none())
        .build()
        .map_err(|_| download_failure(&app, "client", "download_client_unavailable", ""))?;
    let (temporary_path, mut temporary) =
        create_temporary_download(&destination).map_err(|_| {
            download_failure(&app, "destination", "download_destination_unwritable", "")
        })?;
    let transfer_result = async {
        let mut response = client
            .get(download_url)
            .header(reqwest::header::COOKIE, cookie_header)
            .send()
            .await
            .map_err(|_| download_failure(&app, "request", "download_request_failed", ""))?;
        if !response.status().is_success() {
            let detail = format!("http_status={}", response.status().as_u16());
            return Err(download_failure(
                &app,
                "response",
                "download_response_unavailable",
                &detail,
            ));
        }
        while let Some(chunk) = response
            .chunk()
            .await
            .map_err(|_| download_failure(&app, "transfer", "download_interrupted", ""))?
        {
            temporary
                .write_all(&chunk)
                .map_err(|_| download_failure(&app, "write", "download_write_failed", ""))?;
        }
        temporary
            .flush()
            .and_then(|_| temporary.sync_all())
            .map_err(|_| download_failure(&app, "write", "download_write_failed", "sync"))
    }
    .await;
    drop(temporary);
    let result = transfer_result.and_then(|_| {
        replace_download(&temporary_path, &destination)
            .map_err(|_| download_failure(&app, "finalize", "download_finalize_failed", ""))
    });
    if result.is_err() {
        let _ = fs::remove_file(&temporary_path);
    } else {
        append_desktop_log(&app, "download", "succeeded", "");
    }
    result.map(|_| true)
}

#[tauri::command]
async fn save_text_file(
    app: AppHandle,
    suggested_name: String,
    contents: String,
) -> Result<bool, String> {
    let file_name = download_file_name(&suggested_name, "text/plain")?;
    let selected = app
        .dialog()
        .file()
        .set_title("Save ShotForward file")
        .set_file_name(file_name)
        .blocking_save_file();
    let Some(selected) = selected else {
        return Ok(false);
    };
    let destination = selected
        .into_path()
        .map_err(|_| "the selected destination is not a local filesystem path".to_owned())?;
    let (temporary_path, mut temporary) = create_temporary_download(&destination)?;
    let write_result = temporary
        .write_all(contents.as_bytes())
        .and_then(|_| temporary.flush())
        .and_then(|_| temporary.sync_all())
        .map_err(|_| "cannot write the selected file".to_owned());
    drop(temporary);
    let result = write_result.and_then(|_| replace_download(&temporary_path, &destination));
    if result.is_err() {
        let _ = fs::remove_file(&temporary_path);
    }
    result.map(|_| true)
}

fn validate_download_url(current: &Url, raw: &str) -> Result<Url, String> {
    let parsed = current
        .join(raw)
        .map_err(|_| "invalid local download URL".to_owned())?;
    let loopback = match parsed.host() {
        Some(Host::Ipv4(value)) => value.is_loopback(),
        Some(Host::Ipv6(value)) => value.is_loopback(),
        _ => false,
    };
    let segments = parsed
        .path_segments()
        .map(|values| values.collect::<Vec<_>>())
        .unwrap_or_default();
    let content_route = segments.len() == 7
        && segments[0] == "api"
        && segments[1] == "v1"
        && segments[2] == "projects"
        && matches!(segments[4], "artifacts" | "exports")
        && segments[6] == "content";
    if parsed.scheme() != "http"
        || !loopback
        || parsed.origin() != current.origin()
        || !parsed.username().is_empty()
        || parsed.password().is_some()
        || parsed.query().is_some()
        || parsed.fragment().is_some()
        || !content_route
    {
        return Err("local download URL failed validation".to_owned());
    }
    Ok(parsed)
}

fn session_cookie_header<'a>(
    cookies: impl IntoIterator<Item = (&'a str, &'a str)>,
) -> Option<String> {
    cookies
        .into_iter()
        .find(|(name, _)| *name == SESSION_COOKIE_NAME)
        .map(|(_, value)| format!("{SESSION_COOKIE_NAME}={value}"))
}

fn download_file_name(suggested: &str, content_type: &str) -> Result<String, String> {
    let value = suggested.trim();
    if value.is_empty()
        || value == "."
        || value == ".."
        || value.len() > 240
        || value.chars().any(|character| character.is_control())
        || value.contains('/')
        || value.contains('\\')
        || Path::new(value).file_name().and_then(|name| name.to_str()) != Some(value)
    {
        return Err("invalid suggested download name".to_owned());
    }
    if Path::new(value).extension().is_some() {
        return Ok(value.to_owned());
    }
    let extension = match content_type.split(';').next().unwrap_or("").trim() {
        "video/mp4" => ".mp4",
        "image/png" => ".png",
        "image/jpeg" => ".jpg",
        "image/webp" => ".webp",
        "application/json" => ".json",
        _ => "",
    };
    Ok(value.to_owned() + extension)
}

fn create_temporary_download(destination: &Path) -> Result<(PathBuf, File), String> {
    let parent = destination
        .parent()
        .filter(|path| !path.as_os_str().is_empty())
        .ok_or_else(|| "the selected destination has no parent folder".to_owned())?;
    destination
        .file_name()
        .ok_or_else(|| "the selected destination has an invalid file name".to_owned())?;
    let timestamp = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map_err(|_| "the system clock is invalid".to_owned())?
        .as_nanos();
    for index in 0..100_u32 {
        let candidate = parent.join(format!(".shotforward-download-{timestamp}-{index}.part"));
        match OpenOptions::new()
            .write(true)
            .create_new(true)
            .open(&candidate)
        {
            Ok(file) => return Ok((candidate, file)),
            Err(error) if error.kind() == std::io::ErrorKind::AlreadyExists => continue,
            Err(_) => return Err("cannot create a temporary download file".to_owned()),
        }
    }
    Err("cannot allocate a temporary download file".to_owned())
}

fn replace_download(temporary: &Path, destination: &Path) -> Result<(), String> {
    if destination.exists() {
        fs::remove_file(destination).map_err(|_| "cannot replace the selected file".to_owned())?;
    }
    fs::rename(temporary, destination).map_err(|_| "cannot finalize the selected file".to_owned())
}

fn validate_external_url(url: &str) -> Result<Url, String> {
    let parsed = Url::parse(url).map_err(|_| "invalid external URL".to_owned())?;
    if !matches!(parsed.scheme(), "http" | "https")
        || parsed.host().is_none()
        || !parsed.username().is_empty()
        || parsed.password().is_some()
    {
        return Err("external URL failed validation".to_owned());
    }
    Ok(parsed)
}

#[derive(Debug, Deserialize)]
struct BootstrapAnnouncement {
    address: String,
    pairing_code: String,
    instance_id: String,
}

fn validate_announcement(raw: &[u8]) -> Result<(Url, BootstrapAnnouncement), String> {
    let announcement: BootstrapAnnouncement =
        serde_json::from_slice(raw).map_err(|_| "invalid startup announcement".to_owned())?;
    let address = Url::parse(&announcement.address)
        .map_err(|_| "invalid local service address".to_owned())?;
    let loopback = match address.host() {
        Some(Host::Ipv4(value)) => value.is_loopback(),
        Some(Host::Ipv6(value)) => value.is_loopback(),
        _ => false,
    };
    if address.scheme() != "http"
        || !loopback
        || address.port().is_none()
        || address.username() != ""
        || address.password().is_some()
        || address.path() != "/"
        || address.query().is_some()
        || address.fragment().is_some()
    {
        return Err("local service address failed validation".to_owned());
    }
    if announcement.pairing_code.len() != 6
        || !announcement
            .pairing_code
            .bytes()
            .all(|value| value.is_ascii_uppercase() || value.is_ascii_digit())
        || announcement.instance_id.is_empty()
        || announcement.instance_id.len() > 256
    {
        return Err("startup credentials failed validation".to_owned());
    }
    Ok((address, announcement))
}

fn sidecar_path() -> Result<OsString, String> {
    let executable = std::env::current_exe().map_err(|_| "cannot locate desktop executable")?;
    let directory = executable
        .parent()
        .ok_or("desktop executable has no parent directory")?;
    let mut paths = vec![PathBuf::from(directory)];
    if let Some(current) = std::env::var_os("PATH") {
        paths.extend(std::env::split_paths(&current));
    }
    std::env::join_paths(paths).map_err(|_| "cannot construct sidecar search path".to_owned())
}

fn embedded_codex_path() -> Result<Option<PathBuf>, String> {
    let executable = std::env::current_exe().map_err(|_| "cannot locate desktop executable")?;
    let directory = executable
        .parent()
        .ok_or("desktop executable has no parent directory")?;
    let name = if cfg!(windows) {
        "shotforward-codex-runtime.exe"
    } else {
        "shotforward-codex-runtime"
    };
    let candidate = directory.join(name);
    Ok(candidate.is_file().then_some(candidate))
}

fn open_daemon_log(app: &AppHandle) -> Result<File, String> {
    let directory = app
        .path()
        .app_log_dir()
        .map_err(|_| "cannot locate the application log directory".to_owned())?;
    std::fs::create_dir_all(&directory)
        .map_err(|_| "cannot create the application log directory".to_owned())?;
    let current = directory.join("shotforwardd.log");
    if current
        .metadata()
        .map(|metadata| metadata.len() >= MAX_DAEMON_LOG_BYTES)
        .unwrap_or(false)
    {
        let previous = rotated_log_path(&current);
        match std::fs::remove_file(&previous) {
            Ok(()) => {}
            Err(error) if error.kind() == std::io::ErrorKind::NotFound => {}
            Err(_) => return Err("cannot rotate the previous daemon log".to_owned()),
        }
        std::fs::rename(&current, previous)
            .map_err(|_| "cannot rotate the daemon log".to_owned())?;
    }
    let mut file = OpenOptions::new()
        .create(true)
        .append(true)
        .open(current)
        .map_err(|_| "cannot open the daemon log".to_owned())?;
    writeln!(file, "\n--- ShotForward desktop session started ---")
        .map_err(|_| "cannot initialize the daemon log".to_owned())?;
    file.flush()
        .map_err(|_| "cannot initialize the daemon log".to_owned())?;
    Ok(file)
}

fn append_desktop_log(app: &AppHandle, event: &str, status: &str, detail: &str) {
    let Ok(directory) = app.path().app_log_dir() else {
        return;
    };
    if std::fs::create_dir_all(&directory).is_err() {
        return;
    }
    let Ok(mut file) = OpenOptions::new()
        .create(true)
        .append(true)
        .open(directory.join("shotforwardd.log"))
    else {
        return;
    };
    let timestamp = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|duration| duration.as_millis())
        .unwrap_or_default();
    let suffix = if detail.is_empty() {
        String::new()
    } else {
        format!(" detail={detail}")
    };
    let _ = writeln!(
        file,
        "timestamp_ms={timestamp} source=desktop event={event} status={status}{suffix}"
    );
    let _ = file.flush();
}

fn download_failure(app: &AppHandle, stage: &str, code: &str, detail: &str) -> String {
    append_desktop_log(
        app,
        "download_failed",
        code,
        &format!("stage={stage} {detail}"),
    );
    code.to_owned()
}

fn rotated_log_path(current: &Path) -> PathBuf {
    current.with_extension("log.1")
}

fn append_daemon_log(log: &mut Option<File>, chunk: &[u8]) {
    let Some(file) = log.as_mut() else {
        return;
    };
    if file.write_all(chunk).is_err() || file.flush().is_err() {
        *log = None;
        eprintln!("ShotForward daemon logging stopped because the log file is unavailable");
    }
}

fn create_main_window(
    app: &tauri::AppHandle,
    address: Url,
    announcement: BootstrapAnnouncement,
) -> Result<(), String> {
    let allowed_origin = address.origin().ascii_serialization();
    let bootstrap = serde_json::json!({
        "pairingCode": announcement.pairing_code,
        "instanceID": announcement.instance_id,
    });
    let bootstrap_json =
        serde_json::to_string(&bootstrap).map_err(|_| "cannot encode desktop bootstrap")?;
    let initialization_script = format!(
        r#"Object.defineProperty(window, '__SHOTFORWARD_DESKTOP_BOOTSTRAP__', {{ value: {bootstrap_json}, configurable: true }});
Object.defineProperty(window, '__SHOTFORWARD_DESKTOP__', {{ value: Object.freeze({{
  selectProjectParent: () => window.__TAURI_INTERNALS__.invoke('select_project_parent'),
  selectScriptFile: () => window.__TAURI_INTERNALS__.invoke('select_script_file'),
  openExternal: (url) => window.__TAURI_INTERNALS__.invoke('open_external', {{ url }}),
  saveDownload: (url, suggestedName) => window.__TAURI_INTERNALS__.invoke('save_download', {{ url, suggestedName }}),
  saveTextFile: (suggestedName, contents) => window.__TAURI_INTERNALS__.invoke('save_text_file', {{ suggestedName, contents }})
}}) }});"#
    );
    let navigation_origin = allowed_origin.clone();

    WebviewWindowBuilder::new(app, "main", WebviewUrl::External(address))
        .title("ShotForward")
        .inner_size(1440.0, 900.0)
        .min_inner_size(1080.0, 700.0)
        .initialization_script(initialization_script)
        .on_navigation(move |target| target.origin().ascii_serialization() == navigation_origin)
        .build()
        .map_err(|_| "cannot create the ShotForward window".to_owned())?;
    Ok(())
}

fn fail_startup(app: &tauri::AppHandle, message: &str) {
    eprintln!("ShotForward could not start: {message}");
    app.exit(1);
}

fn main() {
    let daemon: Arc<Mutex<Option<CommandChild>>> = Arc::new(Mutex::new(None));
    let daemon_for_setup = Arc::clone(&daemon);
    let daemon_for_exit = Arc::clone(&daemon);

    let application = tauri::Builder::default()
        .plugin(tauri_plugin_shell::init())
        .plugin(tauri_plugin_dialog::init())
        .invoke_handler(tauri::generate_handler![
            select_project_parent,
            select_script_file,
            open_external,
            save_download,
            save_text_file
        ])
        .setup(move |app| {
            let path = sidecar_path().map_err(std::io::Error::other)?;
            let mut daemon_log = match open_daemon_log(app.handle()) {
                Ok(file) => Some(file),
                Err(error) => {
                    eprintln!("ShotForward daemon logging is unavailable: {error}");
                    None
                }
            };
            let mut command = app.shell().sidecar("shotforwardd")?.env("PATH", path);
            if let Some(runtime) = embedded_codex_path().map_err(std::io::Error::other)? {
                command = command.env(
                    "SHOTFORWARD_EMBEDDED_CODEX_EXECUTABLE",
                    runtime.into_os_string(),
                );
            }
            let (mut events, child) = command.spawn()?;
            *daemon_for_setup
                .lock()
                .map_err(|_| std::io::Error::other("daemon lifecycle lock is unavailable"))? =
                Some(child);

            let app_handle = app.handle().clone();
            tauri::async_runtime::spawn(async move {
                let mut stdout = Vec::new();
                let mut ready = false;
                while let Some(event) = events.recv().await {
                    match event {
                        CommandEvent::Stdout(chunk) => {
                            if ready {
                                continue;
                            }
                            stdout.extend_from_slice(&chunk);
                            if stdout.len() > MAX_ANNOUNCEMENT_BYTES {
                                fail_startup(
                                    &app_handle,
                                    "local service returned an oversized startup message",
                                );
                                return;
                            }
                            if let Some(newline) = stdout.iter().position(|value| *value == b'\n') {
                                match validate_announcement(&stdout[..newline]) {
                                    Ok((address, announcement)) => {
                                        if let Err(error) =
                                            create_main_window(&app_handle, address, announcement)
                                        {
                                            fail_startup(&app_handle, &error);
                                            return;
                                        }
                                        ready = true;
                                        stdout.clear();
                                    }
                                    Err(error) => {
                                        fail_startup(&app_handle, &error);
                                        return;
                                    }
                                }
                            }
                        }
                        CommandEvent::Error(_) | CommandEvent::Terminated(_) => {
                            append_daemon_log(
                                &mut daemon_log,
                                b"\nShotForward daemon process stopped unexpectedly\n",
                            );
                            let message = if ready {
                                "local service stopped unexpectedly"
                            } else {
                                "local service exited before it was ready"
                            };
                            fail_startup(&app_handle, message);
                            return;
                        }
                        CommandEvent::Stderr(chunk) => {
                            append_daemon_log(&mut daemon_log, &chunk);
                        }
                        _ => {}
                    }
                }
                let message = if ready {
                    "local service stopped unexpectedly"
                } else {
                    "local service startup channel closed unexpectedly"
                };
                fail_startup(&app_handle, message);
            });
            Ok(())
        })
        .build(tauri::generate_context!())
        .expect("failed to build ShotForward desktop application");

    application.run(move |_app, event| {
        if matches!(event, RunEvent::Exit | RunEvent::ExitRequested { .. }) {
            if let Ok(mut guard) = daemon_for_exit.lock() {
                if let Some(child) = guard.take() {
                    let _ = child.kill();
                }
            }
        }
    });
}

#[cfg(test)]
mod tests {
    use super::{
        download_file_name, script_file_path, session_cookie_header, validate_announcement,
        validate_download_url, validate_external_url,
    };
    use std::path::PathBuf;
    use url::Url;

    #[test]
    fn accepts_a_loopback_startup_announcement() {
        let raw = br#"{"address":"http://127.0.0.1:43100","pairing_code":"AB12CD","instance_id":"instance-one"}"#;
        let (address, announcement) =
            validate_announcement(raw).expect("announcement should be valid");
        assert_eq!(address.as_str(), "http://127.0.0.1:43100/");
        assert_eq!(announcement.pairing_code, "AB12CD");
    }

    #[test]
    fn rejects_non_loopback_addresses_and_credentials_in_urls() {
        for raw in [
            br#"{"address":"https://127.0.0.1:43100","pairing_code":"AB12CD","instance_id":"one"}"#.as_slice(),
            br#"{"address":"http://192.168.1.3:43100","pairing_code":"AB12CD","instance_id":"one"}"#.as_slice(),
            br#"{"address":"http://user@127.0.0.1:43100","pairing_code":"AB12CD","instance_id":"one"}"#.as_slice(),
            br#"{"address":"http://127.0.0.1:43100/?token=secret","pairing_code":"AB12CD","instance_id":"one"}"#.as_slice(),
        ] {
            assert!(validate_announcement(raw).is_err());
        }
    }

    #[test]
    fn rejects_malformed_startup_credentials() {
        for pairing_code in ["ABC12", "abcdef", "ABC-12", "ABC 12", "ABC1234"] {
            let raw = format!(
                r#"{{"address":"http://[::1]:43100","pairing_code":"{pairing_code}","instance_id":"one"}}"#
            );
            assert!(validate_announcement(raw.as_bytes()).is_err());
        }
    }

    #[test]
    fn external_urls_allow_only_http_without_embedded_credentials() {
        assert!(validate_external_url("https://shotforward.com/app/account").is_ok());
        for value in [
            "file:///tmp/private",
            "javascript:alert(1)",
            "https://user:secret@shotforward.com/app/account",
            "https://",
        ] {
            assert!(validate_external_url(value).is_err(), "accepted {value}");
        }
    }

    #[test]
    fn download_urls_are_limited_to_local_artifact_and_export_content() {
        let current = Url::parse("http://127.0.0.1:43100/projects/prj_1").unwrap();
        assert!(
            validate_download_url(&current, "/api/v1/projects/prj_1/artifacts/art_1/content")
                .is_ok()
        );
        assert!(
            validate_download_url(&current, "/api/v1/projects/prj_1/exports/exp_1/content").is_ok()
        );
        for value in [
            "https://example.com/video.mp4",
            "http://127.0.0.1:43101/api/v1/projects/prj_1/artifacts/art_1/content",
            "/api/v1/projects/prj_1/artifacts/art_1/content?token=secret",
            "/api/v1/projects/prj_1",
        ] {
            assert!(
                validate_download_url(&current, value).is_err(),
                "accepted {value}"
            );
        }
    }

    #[test]
    fn download_names_are_safe_and_gain_a_media_extension() {
        assert_eq!(
            download_file_name("shotforward-video", "video/mp4").unwrap(),
            "shotforward-video.mp4"
        );
        assert_eq!(
            download_file_name("diagnostics.json", "text/plain").unwrap(),
            "diagnostics.json"
        );
        for value in ["", "../private.mp4", "folder/video.mp4", "bad\nname.mp4"] {
            assert!(download_file_name(value, "video/mp4").is_err());
        }
    }

    #[test]
    fn download_forwards_only_the_local_session_cookie() {
        let cookies = [
            ("unrelated", "private"),
            ("shotforward_session", "session-value"),
        ];
        assert_eq!(
            session_cookie_header(cookies),
            Some("shotforward_session=session-value".to_owned())
        );
        assert_eq!(session_cookie_header([("unrelated", "private")]), None);
    }

    #[test]
    fn script_picker_accepts_only_markdown_and_text_files() {
        assert!(script_file_path(PathBuf::from("episode.md")).is_ok());
        assert!(script_file_path(PathBuf::from("episode.TXT")).is_ok());
        assert!(script_file_path(PathBuf::from("episode.docx")).is_err());
        assert!(script_file_path(PathBuf::from("episode")).is_err());
    }
}
