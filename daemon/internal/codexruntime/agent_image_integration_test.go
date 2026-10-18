package codexruntime

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	agentapp "github.com/shotforward/shotforward/daemon/internal/application/agent"
)

func TestStartAndSteerUseViewingCopyWithoutChangingTextOrOriginal(t *testing.T) {
	fixture := newNativeCompactionFixture(t)
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewNRGBA(image.Rect(0, 0, 2100, 24))); err != nil {
		t.Fatal(err)
	}
	original := encoded.Bytes()
	source := filepath.Join(fixture.supervisor.options.WorkingDirectory, "reference.png")
	if err := os.WriteFile(source, original, 0o600); err != nil {
		t.Fatal(err)
	}
	const text = "看看 @角色，保留原文，不要生成或修改素材。"
	viewingPaths := []string{}
	fixture.client.handler = func(method string, params map[string]any) (any, error) {
		if method == "turn/start" || method == "turn/steer" {
			input, ok := params["input"].([]map[string]any)
			if !ok || len(input) != 2 || input[0]["text"] != text || input[1]["type"] != "localImage" {
				t.Fatalf("input text or binding was rewritten: %#v", params["input"])
			}
			path, _ := input[1]["path"].(string)
			if path == source || filepath.Dir(path) != filepath.Join(fixture.supervisor.options.WorkingDirectory, agentImageCacheDirectory) {
				t.Fatalf("Runtime did not receive the viewing copy: %q", path)
			}
			viewingPaths = append(viewingPaths, path)
			if method == "turn/start" {
				return map[string]any{"turn": map[string]any{"id": "user-turn"}}, nil
			}
			return map[string]any{"turnId": params["expectedTurnId"]}, nil
		}
		return map[string]any{}, nil
	}
	turn, err := fixture.supervisor.StartTurn(context.Background(), agentapp.TurnRequest{
		CodexThreadID: "thread-a", ModelID: "test-model", Text: text, ImagePaths: []string{source},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.supervisor.SteerTurnWithImages(context.Background(), "thread-a", turn.CodexTurnID, text, []string{source}); err != nil {
		t.Fatal(err)
	}
	if len(viewingPaths) != 2 || viewingPaths[0] != viewingPaths[1] {
		t.Fatalf("same image was not reused: %#v", viewingPaths)
	}
	unchanged, err := os.ReadFile(source)
	if err != nil || !bytes.Equal(unchanged, original) {
		t.Fatalf("source was altered: %v", err)
	}
}
