package projecthttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

type projectMetadataService struct {
	ProjectService
	input  projectapp.UpdateProjectInput
	result projectapp.ProjectResult
}

func (s *projectMetadataService) UpdateProject(_ context.Context, input projectapp.UpdateProjectInput) (projectapp.ProjectResult, error) {
	s.input = input
	return s.result, nil
}

func TestPatchProjectUpdatesEditableMetadata(t *testing.T) {
	projectID, err := project.ParseProjectID("prj_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	if err != nil {
		t.Fatal(err)
	}
	output, err := project.NewOutputSpec(720, 1280)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.September, 20, 8, 0, 0, 0, time.UTC)
	service := &projectMetadataService{result: projectapp.ProjectResult{Project: project.Project{
		ID: projectID, Name: "新名称", Description: "新简介", Output: output,
		Revision: 2, CreatedAt: now, UpdatedAt: now,
	}}}
	handler, err := NewHandler(Options{Projects: service, Catalog: emptyCatalog{}})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPatch, "/api/v1/projects/"+projectID.String(), strings.NewReader(`{"name":"新名称","description":"新简介","expected_revision":1}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("PATCH response = %d %s", response.Code, response.Body.String())
	}
	if service.input.ProjectID != projectID || service.input.Name != "新名称" || service.input.Description != "新简介" || service.input.ExpectedRevision != 1 {
		t.Fatalf("UpdateProject input = %#v", service.input)
	}
	if !strings.Contains(response.Body.String(), `"name":"新名称"`) || !strings.Contains(response.Body.String(), `"revision":2`) {
		t.Fatalf("PATCH body = %s", response.Body.String())
	}
}
