package project

import "testing"

func TestDiagnoseScriptFormat(t *testing.T) {
	t.Parallel()

	markdown := "# 单集标题\n\n## 场景一\n\n正文\n\n### 动作\n\n- 列表\n\n#### 过深标题\n"
	diagnostics := DiagnoseScriptFormat(markdown)
	if len(diagnostics) != 3 {
		t.Fatalf("DiagnoseScriptFormat() = %#v, want 3 diagnostics", diagnostics)
	}
	if diagnostics[0].Code != ScriptDiagnosticUnsupportedHeading || diagnostics[0].Line != 1 {
		t.Fatalf("first diagnostic = %#v", diagnostics[0])
	}
	if diagnostics[1].Code != ScriptDiagnosticUnsupportedBlock || diagnostics[1].Line != 9 {
		t.Fatalf("second diagnostic = %#v", diagnostics[1])
	}
	if diagnostics[2].Code != ScriptDiagnosticUnsupportedHeading || diagnostics[2].Line != 11 {
		t.Fatalf("third diagnostic = %#v", diagnostics[2])
	}
}

func TestDiagnoseScriptFormatReturnsNonNilEmptyResult(t *testing.T) {
	t.Parallel()

	diagnostics := DiagnoseScriptFormat("## 场景一\n\n正文\n\n### 对白\n")
	if diagnostics == nil || len(diagnostics) != 0 {
		t.Fatalf("DiagnoseScriptFormat() = %#v, want non-nil empty result", diagnostics)
	}
}
