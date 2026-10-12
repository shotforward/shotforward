package project

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestValidProjectAndEpisode(t *testing.T) {
	t.Parallel()

	project, episode := validDomainModels(t)
	if err := project.Validate(); err != nil {
		t.Fatalf("Project.Validate() error = %v", err)
	}
	if err := episode.Validate(); err != nil {
		t.Fatalf("Episode.Validate() error = %v", err)
	}
	if err := episode.Summary().Validate(); err != nil {
		t.Fatalf("Episode.Summary().Validate() error = %v", err)
	}
}

func TestProjectValidation(t *testing.T) {
	t.Parallel()

	valid, _ := validDomainModels(t)
	tests := []struct {
		name   string
		mutate func(*Project)
	}{
		{name: "invalid ID", mutate: func(value *Project) { value.ID = "invalid" }},
		{name: "missing name", mutate: func(value *Project) { value.Name = " " }},
		{name: "multiline name", mutate: func(value *Project) { value.Name = "One\nTwo" }},
		{name: "null description", mutate: func(value *Project) { value.Description = "a\x00b" }},
		{name: "invalid output", mutate: func(value *Project) { value.Output = OutputSpec{} }},
		{name: "invalid revision", mutate: func(value *Project) { value.Revision = 0 }},
		{name: "non-UTC time", mutate: func(value *Project) { value.UpdatedAt = value.UpdatedAt.In(time.FixedZone("offset", 3600)) }},
		{name: "sub-millisecond time", mutate: func(value *Project) { value.UpdatedAt = value.UpdatedAt.Add(time.Nanosecond) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := valid
			test.mutate(&value)
			if err := value.Validate(); !errors.Is(err, ErrInvalidProject) {
				t.Fatalf("Validate() error = %v, want ErrInvalidProject", err)
			}
		})
	}
}

func TestEpisodeValidation(t *testing.T) {
	t.Parallel()

	_, valid := validDomainModels(t)
	tests := []struct {
		name   string
		mutate func(*Episode)
	}{
		{name: "invalid ID", mutate: func(value *Episode) { value.ID = "invalid" }},
		{name: "invalid Project ID", mutate: func(value *Episode) { value.ProjectID = "invalid" }},
		{name: "invalid sequence", mutate: func(value *Episode) { value.SequenceNumber = 0 }},
		{name: "missing title", mutate: func(value *Episode) { value.Title = "" }},
		{name: "invalid duration", mutate: func(value *Episode) { value.TargetDuration = time.Nanosecond }},
		{name: "invalid revision", mutate: func(value *Episode) { value.Revision = 0 }},
		{name: "CR in script", mutate: func(value *Episode) { value.Script.Markdown = "a\rb" }},
		{name: "invalid script revision", mutate: func(value *Episode) { value.Script.Revision = 0 }},
		{name: "invalid script time", mutate: func(value *Episode) { value.Script.UpdatedAt = time.Time{} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := valid
			test.mutate(&value)
			if err := value.Validate(); !errors.Is(err, ErrInvalidEpisode) {
				t.Fatalf("Validate() error = %v, want ErrInvalidEpisode", err)
			}
		})
	}
}

func TestEpisodeSummaryValidation(t *testing.T) {
	t.Parallel()

	_, episode := validDomainModels(t)
	summary := episode.Summary()
	summary.ScriptRevision = 0
	if err := summary.Validate(); !errors.Is(err, ErrInvalidEpisode) {
		t.Fatalf("Validate() error = %v, want ErrInvalidEpisode", err)
	}
}

func TestNormalizeMarkdown(t *testing.T) {
	t.Parallel()

	got := NormalizeMarkdown("第一行\r\n第二行\r第三行\n")
	want := "第一行\n第二行\n第三行\n"
	if got != want {
		t.Fatalf("NormalizeMarkdown() = %q, want %q", got, want)
	}
}

func TestScriptCharacterLimitAndUTF8Validation(t *testing.T) {
	t.Parallel()

	_, episode := validDomainModels(t)
	episode.Script.Markdown = strings.Repeat("镜", MaxScriptCharacters)
	if err := episode.Script.Validate(); err != nil {
		t.Fatalf("Validate(%d characters) error = %v", MaxScriptCharacters, err)
	}
	episode.Script.Markdown += "头"
	if err := episode.Script.Validate(); !errors.Is(err, ErrInvalidScript) {
		t.Fatalf("Validate(over limit) error = %v, want ErrInvalidScript", err)
	}
	episode.Script.Markdown = string([]byte{0xff})
	if err := episode.Script.Validate(); !errors.Is(err, ErrInvalidScript) {
		t.Fatalf("Validate(invalid UTF-8) error = %v, want ErrInvalidScript", err)
	}
}

func validDomainModels(t *testing.T) (Project, Episode) {
	t.Helper()
	projectID, err := ParseProjectID("prj_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	if err != nil {
		t.Fatal(err)
	}
	episodeID, err := ParseEpisodeID("ep_234567ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	if err != nil {
		t.Fatal(err)
	}
	output, err := NewOutputSpec(720, 1280)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.September, 2, 12, 0, 0, 123_000_000, time.UTC)
	project := Project{
		ID:          projectID,
		Name:        "雨夜来信",
		Description: "一部雨夜短片",
		Output:      output,
		Revision:    1,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	episode := Episode{
		EpisodeMetadata: EpisodeMetadata{
			ID:             episodeID,
			ProjectID:      projectID,
			SequenceNumber: 1,
			Title:          "第 1 集",
			TargetDuration: 60 * time.Second,
			Revision:       1,
			CreatedAt:      now,
			UpdatedAt:      now,
		},
		Script: Script{
			Markdown:  "",
			Revision:  1,
			UpdatedAt: now,
		},
	}
	return project, episode
}
