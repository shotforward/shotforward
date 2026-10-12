fn main() {
    let manifest = tauri_build::AppManifest::new().commands(&[
        "select_project_parent",
        "select_script_file",
        "open_external",
        "save_download",
        "save_text_file",
    ]);
    tauri_build::try_build(tauri_build::Attributes::new().app_manifest(manifest))
        .expect("failed to build ShotForward desktop application metadata");
}
