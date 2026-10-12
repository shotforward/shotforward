package prompttranslation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type fakeCompleter struct {
	calls int
	fn    func(context.Context, string, string, string) (string, error)
}

func (f *fakeCompleter) Complete(ctx context.Context, model, instructions, text string) (string, error) {
	f.calls++
	return f.fn(ctx, model, instructions, text)
}

func completion(needsTranslation bool, prompt string) string {
	value, _ := json.Marshal(map[string]any{"needs_translation": needsTranslation, "prompt": prompt})
	return string(value)
}

func TestEnglishPromptIsReturnedExactlyAfterOneCompletion(t *testing.T) {
	prompt := "  integrated_multimodal_description:\n<Subject 1> sees <Picture 1>.\n[Shot 2] At 00:04.000, he replies (S1): <d>[Chinese] 好的。</d>\n\noverall_soundscape:\nQuiet.\nnon_diegetic_music:\nNone  "
	worker := &fakeCompleter{fn: func(_ context.Context, model, instructions, text string) (string, error) {
		if model != "selected-model" || !strings.Contains(instructions, "complete supplied generation prompt") {
			t.Fatal("model selection or whole-prompt instructions were not passed")
		}
		for _, protected := range []string{"integrated_multimodal_description:", "<Subject 1>", "<Picture 1>", "[Shot 2]", "00:04.000", "<d>[Chinese] 好的。</d>"} {
			if strings.Contains(text, protected) {
				t.Fatalf("protected fragment exposed to translation: %q", protected)
			}
		}
		return completion(false, text), nil
	}}
	got, err := New(worker).Translate(context.Background(), "selected-model", prompt)
	if err != nil || got != prompt || worker.calls != 1 {
		t.Fatalf("Translate = %q, %v, calls=%d", got, err, worker.calls)
	}
}

func TestWholePromptAllowsReferenceGrammarReorderingAndTranslatesOrdinaryQuotes(t *testing.T) {
	prompt := `先看<Picture 1>，再看<Picture 2>。 <lyrics>月亮代表我的心</lyrics> "红色外套"`
	worker := &fakeCompleter{fn: func(_ context.Context, _, instructions, text string) (string, error) {
		if !strings.Contains(text, `"红色外套"`) || strings.Contains(text, "月亮代表我的心") {
			t.Fatal("ordinary quotes were protected or explicit lyrics were not protected")
		}
		if !strings.Contains(instructions, "even when they lack explicit literal tags") {
			t.Fatal("unmarked dialogue/lyrics/on-screen text preservation instruction is absent")
		}
		return completion(true, "Look at "+marker(1)+" after "+marker(0)+". "+marker(2)+` "a red coat"`), nil
	}}
	got, err := New(worker).Translate(context.Background(), "model", prompt)
	want := `Look at <Picture 2> after <Picture 1>. <lyrics>月亮代表我的心</lyrics> "a red coat"`
	if err != nil || got != want || worker.calls != 1 {
		t.Fatalf("Translate = %q, %v, calls=%d; want %q", got, err, worker.calls, want)
	}
}

func TestExplicitLiteralBlocksIncludingNestedTagsArePreserved(t *testing.T) {
	prompt := "她唱歌。 <d>[Chinese] 你好。</d> <lyrics>春天在哪里</lyrics> <on_screen_text>入口</on_screen_text> <literal><Subject 1> “原文”</literal>"
	worker := &fakeCompleter{fn: func(_ context.Context, _, _, text string) (string, error) {
		for _, literal := range []string{"你好", "春天在哪里", "入口", "原文"} {
			if strings.Contains(text, literal) {
				t.Fatalf("literal leaked into translatable text: %s", literal)
			}
		}
		return completion(true, strings.Replace(text, "她唱歌。", "She sings.", 1)), nil
	}}
	got, err := New(worker).Translate(context.Background(), "model", prompt)
	if err != nil || got != strings.Replace(prompt, "她唱歌。", "She sings.", 1) {
		t.Fatalf("Translate = %q, %v", got, err)
	}
}

func TestLegacyPlainTextIsNotGivenStructuredSections(t *testing.T) {
	worker := &fakeCompleter{fn: func(_ context.Context, _, _, text string) (string, error) {
		if text != "她转身，随后说“你好”。" {
			t.Fatalf("legacy text was rewritten before completion: %q", text)
		}
		return completion(true, "She turns, then says “你好”."), nil
	}}
	got, err := New(worker).Translate(context.Background(), "model", "她转身，随后说“你好”。")
	if err != nil || got != "She turns, then says “你好”." {
		t.Fatalf("Translate = %q, %v", got, err)
	}
}

func TestJSONPromptKeepsKeysAndTranslatesQuotedDescription(t *testing.T) {
	prompt := `{"summary":"她走进房间。","overall_soundscape":"安静。"}`
	worker := &fakeCompleter{fn: func(_ context.Context, _, _, text string) (string, error) {
		if !json.Valid([]byte(text)) || !strings.Contains(text, "她走进房间。") {
			t.Fatalf("protected JSON is invalid or quoted body was protected: %s", text)
		}
		return completion(true, strings.NewReplacer("她走进房间。", "She enters the room.", "安静。", "Quiet.").Replace(text)), nil
	}}
	got, err := New(worker).Translate(context.Background(), "model", prompt)
	if err != nil || got != `{"summary":"She enters the room.","overall_soundscape":"Quiet."}` {
		t.Fatalf("Translate = %q, %v", got, err)
	}
}

func TestInvalidWorkerResponsesAreTypedValidationErrors(t *testing.T) {
	tests := []struct {
		name     string
		response func(string) string
	}{
		{"not JSON", func(string) string { return "Here is the translation." }},
		{"invalid UTF-8", func(string) string { return "{\"needs_translation\":true,\"prompt\":\"" + string([]byte{0xff}) + "\"}" }},
		{"fenced JSON", func(text string) string { return "```json\n" + completion(true, text) + "\n```" }},
		{"missing field", func(string) string { return `{"prompt":"English"}` }},
		{"unknown field", func(string) string { return `{"needs_translation":true,"prompt":"English","extra":1}` }},
		{"duplicate field", func(string) string { return `{"needs_translation":true,"needs_translation":false,"prompt":"English"}` }},
		{"null boolean", func(string) string { return `{"needs_translation":null,"prompt":"English"}` }},
		{"null prompt", func(string) string { return `{"needs_translation":true,"prompt":null}` }},
		{"trailing JSON", func(text string) string { return completion(true, text) + `{}` }},
		{"false rewriting", func(text string) string { return completion(false, text+" changed") }},
		{"missing fragment", func(text string) string { return completion(true, strings.Replace(text, marker(0), "", 1)) }},
		{"duplicate fragment", func(text string) string { return completion(true, text+marker(0)) }},
		{"unknown fragment", func(text string) string { return completion(true, text+marker(99)) }},
		{"new reference", func(text string) string { return completion(true, text+" <Picture 99>") }},
		{"new tag", func(text string) string { return completion(true, text+" <script>") }},
		{"new header", func(text string) string { return completion(true, text+"\nsummary: Added.") }},
		{"new shot", func(text string) string { return completion(true, text+" [Shot 99]") }},
		{"new timestamp", func(text string) string { return completion(true, text+" 00:12.000") }},
		{"fenced prompt", func(text string) string { return completion(true, "```\n"+text+"\n```") }},
		{"new paragraph", func(text string) string { return completion(true, text+"\n\nAdded.") }},
		{"control character", func(text string) string { return completion(true, text+"\x00") }},
		{"empty text", func(string) string { return completion(true, "") }},
		{"removed description", func(string) string { return completion(true, marker(0)) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			worker := &fakeCompleter{fn: func(_ context.Context, _, _, text string) (string, error) {
				return test.response(text), nil
			}}
			_, err := New(worker).Translate(context.Background(), "model", "她看着<Picture 1>。")
			var validation *ValidationError
			if !errors.As(err, &validation) || worker.calls != 1 {
				t.Fatalf("error = %v, calls=%d", err, worker.calls)
			}
		})
	}
}

func TestProtectedContentCannotMoveAcrossParagraphHeaderOrShot(t *testing.T) {
	for _, prompt := range []string{
		"她看着<Picture 1>。\n\n他离开。",
		"summary:\n她看着<Picture 1>。\noverall_soundscape:\n安静。",
		"她看着<Picture 1>。[Shot 2] 他离开。",
	} {
		t.Run(prompt, func(t *testing.T) {
			worker := &fakeCompleter{fn: func(_ context.Context, _, _, text string) (string, error) {
				protected, err := protect(prompt)
				if err != nil {
					t.Fatal(err)
				}
				for _, token := range protected.tokens {
					if token.value == "<Picture 1>" {
						return completion(true, strings.Replace(text, token.marker, "", 1)+token.marker), nil
					}
				}
				t.Fatal("reference marker not found")
				return "", nil
			}}
			_, err := New(worker).Translate(context.Background(), "model", prompt)
			var validation *ValidationError
			if !errors.As(err, &validation) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestBoundaryOrderAndExistingJSONFormatAreValidated(t *testing.T) {
	tests := []struct{ prompt, output string }{
		{"summary:\n一。\noverall_soundscape:\n二。", marker(1) + "\nTwo.\n" + marker(0) + "\nOne."},
		{`{"summary":"她走。"}`, marker(0) + " She walks."},
	}
	for _, test := range tests {
		worker := &fakeCompleter{fn: func(_ context.Context, _, _, _ string) (string, error) {
			return completion(true, test.output), nil
		}}
		_, err := New(worker).Translate(context.Background(), "model", test.prompt)
		var validation *ValidationError
		if !errors.As(err, &validation) {
			t.Fatalf("error = %v for %q", err, test.prompt)
		}
	}
}

func TestJSONKeysTypesAndContainerStructureCannotChange(t *testing.T) {
	prompt := `{"description":"她走。","shots":["她停下。"],"duration":5,"audio":true}`
	for _, output := range []string{
		`{"translated_description":"She walks.","shots":["She stops."],"duration":5,"audio":true}`,
		`{"description":"She walks.","shots":"She stops.","duration":5,"audio":true}`,
		`{"description":"She walks.","shots":["She stops.","She smiles."],"duration":5,"audio":true}`,
		`{"description":"She walks.","shots":["She stops."],"duration":6,"audio":true}`,
		`{"description":"She walks.","shots":["She stops."],"duration":5,"audio":false}`,
	} {
		worker := &fakeCompleter{fn: func(context.Context, string, string, string) (string, error) {
			return completion(true, output), nil
		}}
		_, err := New(worker).Translate(context.Background(), "model", prompt)
		var validation *ValidationError
		if !errors.As(err, &validation) {
			t.Fatalf("error = %v for %s", err, output)
		}
	}
}

func TestSingleNewlinesArePreservedAndCannotBeAddedOrRemoved(t *testing.T) {
	prompt := "summary:\n她转身。\noverall_soundscape:\n安静。"
	for _, invalid := range []string{"", "drop", "add"} {
		t.Run(invalid, func(t *testing.T) {
			worker := &fakeCompleter{fn: func(_ context.Context, _, _, text string) (string, error) {
				if strings.Contains(text, "\n") {
					t.Fatal("original newline was not protected")
				}
				translated := strings.NewReplacer("她转身。", "She turns.", "安静。", "Quiet.").Replace(text)
				if invalid == "drop" {
					protected, err := protect(prompt)
					if err != nil {
						t.Fatal(err)
					}
					for _, token := range protected.tokens {
						if token.value == "\n" {
							translated = strings.Replace(translated, token.marker, "", 1)
							break
						}
					}
				} else if invalid == "add" {
					translated += "\n"
				}
				return completion(true, translated), nil
			}}
			got, err := New(worker).Translate(context.Background(), "model", prompt)
			if invalid != "" {
				var validation *ValidationError
				if !errors.As(err, &validation) {
					t.Fatalf("expected validation failure, got %q, %v", got, err)
				}
			} else if err != nil || got != "summary:\nShe turns.\noverall_soundscape:\nQuiet." {
				t.Fatalf("Translate = %q, %v", got, err)
			}
		})
	}
}

func TestNestedJSONUnknownKeysAndEscapedNewlinesStayInPlace(t *testing.T) {
	prompt := `{"metadata":{"first":"一。","second":{"lines":"二。\n三。"}},"description":"四。"}`
	outputs := []struct {
		prompt string
		valid  bool
	}{
		{`{"metadata":{"first":"One.","second":{"lines":"Two.\nThree."}},"description":"Four."}`, true},
		{`{"metadata":{"second":{"lines":"Two.\nThree."},"first":"One."},"description":"Four."}`, false},
		{`{"metadata":{"first":"One.","second":{"renamed":"Two.\nThree."}},"description":"Four."}`, false},
		{`{"metadata":{"first":"One.","second":{"lines":"Two. Three."}},"description":"Four."}`, false},
		{`{"metadata":{"first":"One.\n","second":{"lines":"Two.\nThree."}},"description":"Four."}`, false},
	}
	for _, output := range outputs {
		worker := &fakeCompleter{fn: func(context.Context, string, string, string) (string, error) {
			return completion(true, output.prompt), nil
		}}
		got, err := New(worker).Translate(context.Background(), "model", prompt)
		if output.valid {
			if err != nil || got != output.prompt {
				t.Fatalf("Translate = %q, %v", got, err)
			}
		} else {
			var validation *ValidationError
			if !errors.As(err, &validation) {
				t.Fatalf("expected validation failure, got %q, %v", got, err)
			}
		}
	}
}

func TestOfficialRetentionRelationshipValuesAreProtected(t *testing.T) {
	for _, relationship := range []string{"fully_preserved", "partially_preserved", "attribute_transfer", "weak_reference", "fully_copy", "partially_copy", "reference"} {
		t.Run(relationship, func(t *testing.T) {
			label := "<Audio 1>"
			if relationship == "fully_preserved" || relationship == "partially_preserved" || relationship == "attribute_transfer" {
				label = "<Subject 1>"
			}
			prompt := "retention_analysis:\n" + label + ": " + relationship + " - 保留特征。"
			for _, modify := range []bool{false, true} {
				worker := &fakeCompleter{fn: func(_ context.Context, _, _, text string) (string, error) {
					if strings.Contains(text, relationship) {
						t.Fatal("fixed relationship value was not protected")
					}
					translated := strings.Replace(text, "保留特征。", "Keep the characteristics.", 1)
					if modify {
						protected, err := protect(prompt)
						if err != nil {
							t.Fatal(err)
						}
						for _, token := range protected.tokens {
							if token.value == relationship {
								translated = strings.Replace(translated, token.marker, "a different relationship", 1)
							}
						}
					}
					return completion(true, translated), nil
				}}
				got, err := New(worker).Translate(context.Background(), "model", prompt)
				if modify {
					var validation *ValidationError
					if !errors.As(err, &validation) {
						t.Fatalf("modified enum accepted: %q, %v", got, err)
					}
				} else if err != nil || got != strings.Replace(prompt, "保留特征。", "Keep the characteristics.", 1) {
					t.Fatalf("Translate = %q, %v", got, err)
				}
			}
		})
	}
	protected, err := protect("这个reference只是普通描述。")
	if err != nil || !strings.Contains(protected.text, "reference") {
		t.Fatalf("ordinary reference prose was protected: %q, %v", protected.text, err)
	}
}

func TestInvalidInputFailsBeforeCallingWorker(t *testing.T) {
	for _, prompt := range []string{"", "  ", "a\r\nb", "a\x00b", string([]byte{0xff}), "<d>你好", "</lyrics>", "<d><literal>你好</d></literal>", marker(0)} {
		t.Run(prompt, func(t *testing.T) {
			worker := &fakeCompleter{fn: func(context.Context, string, string, string) (string, error) {
				t.Fatal("worker called for invalid input")
				return "", nil
			}}
			_, err := New(worker).Translate(context.Background(), "model", prompt)
			var validation *ValidationError
			if !errors.As(err, &validation) || worker.calls != 0 {
				t.Fatalf("error = %v, calls=%d", err, worker.calls)
			}
		})
	}
}

func TestWorkerErrorsAndCancellationAreTypedAndUnwrap(t *testing.T) {
	cause := errors.New("private worker detail")
	worker := &fakeCompleter{fn: func(context.Context, string, string, string) (string, error) { return "", cause }}
	_, err := New(worker).Translate(context.Background(), "model", "English")
	var failure *WorkerError
	if !errors.As(err, &failure) || !errors.Is(err, cause) || strings.Contains(err.Error(), "private") {
		t.Fatalf("error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = New(worker).Translate(ctx, "model", "English")
	if !errors.As(err, &failure) || !errors.Is(err, context.Canceled) || worker.calls != 1 {
		t.Fatalf("cancelled error = %v, calls=%d", err, worker.calls)
	}
	ctx, cancel = context.WithCancel(context.Background())
	worker.fn = func(_ context.Context, _, _, text string) (string, error) {
		cancel()
		return completion(false, text), nil
	}
	_, err = New(worker).Translate(ctx, "model", "English")
	if !errors.As(err, &failure) || !errors.Is(err, context.Canceled) || worker.calls != 2 {
		t.Fatalf("cancelled completion error = %v, calls=%d", err, worker.calls)
	}
}

func TestPromptLengthIsLeftToCaller(t *testing.T) {
	prompt := strings.Repeat("English prose. ", 3000)
	worker := &fakeCompleter{fn: func(_ context.Context, _, _, text string) (string, error) { return completion(false, text), nil }}
	got, err := New(worker).Translate(context.Background(), "model", prompt)
	if err != nil || got != prompt {
		t.Fatalf("Translate error = %v", err)
	}
}
