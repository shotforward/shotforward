package projecthttp

import (
	"net/http"

	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

func (h *Handler) serveArtifactContentRoute(response http.ResponseWriter, request *http.Request, segments []string) bool {
	if len(segments) != 7 || segments[0] != "api" || segments[1] != "v1" || segments[2] != "projects" || segments[4] != "artifacts" || segments[6] != "content" {
		return false
	}
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		methodNotAllowed(response)
		return true
	}
	projectID, err := project.ParseProjectID(segments[3])
	if err != nil {
		writeMappedError(response, projectapp.ErrInvalidRequest)
		return true
	}
	artifactID, err := project.ParseArtifactID(segments[5])
	if err != nil {
		writeMappedError(response, projectapp.ErrInvalidRequest)
		return true
	}
	source, err := h.projects.GetArtifactContent(request.Context(), projectapp.GetArtifactContentInput{ProjectID: projectID, ArtifactID: artifactID})
	if err != nil {
		writeMappedError(response, err)
		return true
	}
	defer source.Content.Close()
	response.Header().Set("Content-Type", source.MIMEType)
	response.Header().Set("Accept-Ranges", "bytes")
	http.ServeContent(response, request, source.OriginalFilename, source.ModifiedAt, source.Content)
	return true
}
