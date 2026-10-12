package host

import (
	"context"
	"crypto/rand"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shotforward/shotforward/daemon/internal/codexruntime"
	"github.com/shotforward/shotforward/daemon/internal/config"
	"github.com/shotforward/shotforward/daemon/internal/platformpaths"
	"github.com/shotforward/shotforward/daemon/internal/prompttranslation"
	"github.com/shotforward/shotforward/daemon/internal/securecredentials"
	"github.com/shotforward/shotforward/daemon/internal/shotforwardcloud"
)

// Opt-in only: this makes one real, billable text completion per service. It
// never opens a project, registers project MCP, or submits video generation.
func TestLiveWholePromptTranslation(t *testing.T) {
	if os.Getenv("SHOTFORWARD_LIVE_TRANSLATION") != "1" {
		t.Skip("real Agent requests require explicit authorization")
	}
	directories, err := platformpaths.ResolveApplicationDirs()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(os.LookupEnv)
	if err != nil {
		t.Fatal(err)
	}
	for _, service := range []string{"local", "cloud"} {
		t.Run(service, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			options := codexruntime.TextWorkerOptions{ConfiguredExecutable: cfg.CodexExecutable, WorkingDirectory: filepath.Clean(t.TempDir()), Environment: os.Environ(), NetworkSettingsPath: filepath.Join(directories.Data, "codex-network.json"), ClientVersion: "shotforward-translation-smoke", MinimumVersion: codexruntime.MinimumSupportedUserCodexVersion}
			model := os.Getenv("SHOTFORWARD_TRANSLATION_LOCAL_MODEL")
			if service == "cloud" {
				options.MinimumVersion = ""
				manager, err := shotforwardcloud.NewManagerWithWebURL(cfg.CloudAPIURL, cfg.CloudWebURL, securecredentials.NewStore(directories.Data), shotforwardcloud.NewNetworkHTTPClient(filepath.Join(directories.Data, "codex-network.json")))
				if err != nil {
					t.Fatal(err)
				}
				models, err := manager.AgentModels(ctx)
				if err != nil {
					t.Fatalf("cloud model discovery: %v", err)
				}
				model = ""
				for _, candidate := range models {
					if candidate.Hidden {
						continue
					}
					if model == "" {
						model = candidate.ID
					}
					if strings.Contains(strings.ToLower(candidate.ID), "luna") {
						model = candidate.ID
						break
					}
				}
				proxyToken, err := randomIdentifier(rand.Reader)
				if err != nil {
					t.Fatal(err)
				}
				proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("Authorization") != "Bearer "+proxyToken {
						http.Error(w, "unauthorized", http.StatusUnauthorized)
						return
					}
					manager.ProxyOpenAI(w, r, r.URL.Path)
				}))
				defer proxy.Close()
				cfg.CloudProxyURL, cfg.CloudProxyToken = proxy.URL, proxyToken
				options.ConfiguredExecutable = cfg.EmbeddedCodexExecutable
				options.Environment = isolatedEnvironment(os.Environ(), map[string]string{"CODEX_HOME": options.WorkingDirectory, "SHOTFORWARD_EMBEDDED_API_KEY": proxyToken})
				options.NetworkSettingsPath = ""
				options.AdditionalConfig = shotForwardCodexProviderConfig(cfg)
				options.DirectToolModelCatalogPath = filepath.Join(options.WorkingDirectory, "models.json")
				options.BeginTurn = manager.BeginAgentTurn
			}
			if model == "" {
				t.Fatal("no explicit/available translation model")
			}
			worker, err := codexruntime.NewTextWorker(options)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = worker.Close(closeCtx)
			}()
			prompt := "subject_definitions:\n<Subject 1> 是穿蓝色外套的小熊，参考 <Picture 1>。\nsummary:\n小熊在花园慢慢走向镜头。\nretention_analysis:\n<Subject 1> -> <Picture 1>: fully_preserved\ndetailed_description:\n[Shot 1] <Subject 1> 挥手并说 <d>[Chinese]你好，今天真开心！</d>。不要改变服装，不要增加人物。\n[Shot 2] At 00:04.000 <Subject 1> 微笑。\noverall_soundscape:\n轻柔的风声，无其他对白。\nnon_diegetic_music:\n没有背景音乐。"
			started := time.Now()
			translated, err := prompttranslation.New(worker).Translate(ctx, model, prompt)
			if err != nil {
				var failed *prompttranslation.WorkerError
				if errors.As(err, &failed) {
					t.Fatalf("%s isolated worker (%T): %v", service, failed.Err, failed.Err)
				}
				t.Fatalf("%s isolated translation: %v", service, err)
			}
			for _, literal := range []string{"<Subject 1>", "<Picture 1>", "00:04.000", "fully_preserved", "<d>[Chinese]你好，今天真开心！</d>"} {
				if strings.Count(translated, literal) != strings.Count(prompt, literal) {
					t.Fatalf("protected content changed: %s", literal)
				}
			}
			for _, prose := range []string{"穿蓝色外套", "不要改变服装", "轻柔的风声", "没有背景音乐"} {
				if strings.Contains(translated, prose) {
					t.Fatalf("descriptive prose was not translated: %s", prose)
				}
			}
			t.Logf("%s model=%s elapsed=%s\n%s", service, model, time.Since(started).Round(time.Millisecond), translated)
		})
	}
}
