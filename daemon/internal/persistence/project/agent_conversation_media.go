package projectstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

func (s *Store) AgentConversationMediaList(ctx context.Context, episodeID domain.EpisodeID, sessionID string) ([]domain.AgentConversationMedia, error) {
	return s.agentConversationMedia(ctx, episodeID, sessionID, "")
}

func (s *Store) AgentConversationMediaForOperation(ctx context.Context, episodeID domain.EpisodeID, sessionID, operationID string) ([]domain.AgentConversationMedia, error) {
	if operationID == "" {
		return nil, domain.ErrInvalidAgentConversationMedia
	}
	return s.agentConversationMedia(ctx, episodeID, sessionID, operationID)
}

func (s *Store) agentConversationMedia(ctx context.Context, episodeID domain.EpisodeID, sessionID, operationID string) ([]domain.AgentConversationMedia, error) {
	database, err := s.requireDatabase()
	if err != nil {
		return nil, err
	}
	if validateContext(ctx) != nil || episodeID.Validate() != nil || sessionID == "" {
		return nil, domain.ErrInvalidAgentConversationMedia
	}
	query := `SELECT m.session_id,m.client_operation_id,m.turn_id,m.name,m.origin,m.created_at_ms,` + artifactSelectColumns("a") + `
FROM agent_conversation_media m JOIN artifacts a ON a.artifact_id=m.artifact_id
WHERE m.episode_id=? AND m.session_id=?`
	arguments := []any{episodeID.String(), sessionID}
	if operationID != "" {
		query += ` AND m.client_operation_id=?`
		arguments = append(arguments, operationID)
	}
	query += ` ORDER BY m.created_at_ms,m.artifact_id`
	rows, err := database.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, fmt.Errorf("list Agent conversation media: %w", err)
	}
	defer rows.Close()
	result := make([]domain.AgentConversationMedia, 0)
	for rows.Next() {
		var value domain.AgentConversationMedia
		var origin string
		var createdMS, artifactCreatedMS int64
		var artifactID, projectID, kind, relativePath, sha, mime, filename string
		var byteSize, duration, fpsN, fpsD int64
		var width, height, sampleRate, channels int
		var remoteURL sql.NullString
		var remoteExpires sql.NullInt64
		if err := rows.Scan(&value.SessionID, &value.ClientOperationID, &value.TurnID, &value.Name, &origin, &createdMS,
			&artifactID, &projectID, &kind, &relativePath, &sha, &byteSize, &mime, &filename, &width, &height,
			&duration, &fpsN, &fpsD, &sampleRate, &channels, &remoteURL, &remoteExpires, &artifactCreatedMS); err != nil {
			return nil, err
		}
		parsedArtifactID, parseErr := domain.ParseArtifactID(artifactID)
		if parseErr != nil {
			return nil, parseErr
		}
		parsedProjectID, parseErr := domain.ParseProjectID(projectID)
		if parseErr != nil {
			return nil, parseErr
		}
		artifact := domain.Artifact{ID: parsedArtifactID, ProjectID: parsedProjectID, Kind: domain.MediaKind(kind), RelativePath: relativePath,
			SHA256: sha, ByteSize: byteSize, MIMEType: mime, OriginalFilename: filename, Width: width, Height: height,
			DurationMilliseconds: duration, FPSNumerator: fpsN, FPSDenominator: fpsD, AudioSampleRate: sampleRate,
			AudioChannels: channels, RemoteReferenceURL: remoteURL.String, CreatedAt: time.UnixMilli(artifactCreatedMS).UTC()}
		if remoteExpires.Valid {
			expires := time.UnixMilli(remoteExpires.Int64).UTC()
			artifact.RemoteReferenceExpiresAt = &expires
		}
		value.EpisodeID, value.Origin, value.CreatedAt, value.Artifact = episodeID, domain.AgentConversationMediaOrigin(origin), time.UnixMilli(createdMS).UTC(), artifact
		if err := value.Validate(); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (s *Store) PutAgentConversationMedia(ctx context.Context, value domain.AgentConversationMedia, updatedAt time.Time) error {
	if value.Validate() != nil || validateMutationTime(updatedAt) != nil || updatedAt.Before(value.CreatedAt) {
		return domain.ErrInvalidAgentConversationMedia
	}
	return s.withTransaction(ctx, "put Agent conversation media", func(tx *sql.Tx) error {
		var episodeProjectID string
		if err := tx.QueryRowContext(ctx, `SELECT project_id FROM episodes WHERE episode_id=?`, value.EpisodeID.String()).Scan(&episodeProjectID); err != nil {
			return err
		}
		if episodeProjectID != value.Artifact.ProjectID.String() {
			return errors.New("Agent conversation media belongs to another Project")
		}
		stored, err := transactionArtifact(ctx, tx, value.Artifact.ID)
		if errors.Is(err, ErrArtifactNotFound) {
			if err := insertArtifact(ctx, tx, value.Artifact); err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else if stored.ProjectID != value.Artifact.ProjectID || stored.SHA256 != value.Artifact.SHA256 {
			return errors.New("Agent conversation Artifact identity conflict")
		}
		_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO agent_conversation_media
(episode_id,session_id,client_operation_id,turn_id,artifact_id,name,origin,created_at_ms)
VALUES (?,?,?,?,?,?,?,?)`, value.EpisodeID.String(), value.SessionID, value.ClientOperationID, value.TurnID,
			value.Artifact.ID.String(), value.Name, string(value.Origin), value.CreatedAt.UnixMilli())
		return err
	})
}

func (s *Store) BindAgentConversationMedia(ctx context.Context, episodeID domain.EpisodeID, sessionID, operationID, turnID string, artifactIDs []domain.ArtifactID, updatedAt time.Time) error {
	if episodeID.Validate() != nil || sessionID == "" || operationID == "" || turnID == "" || len(artifactIDs) == 0 || validateMutationTime(updatedAt) != nil {
		return domain.ErrInvalidAgentConversationMedia
	}
	return s.withTransaction(ctx, "bind Agent conversation media", func(tx *sql.Tx) error {
		for _, artifactID := range artifactIDs {
			if artifactID.Validate() != nil {
				return domain.ErrInvalidAgentConversationMedia
			}
			result, err := tx.ExecContext(ctx, `UPDATE agent_conversation_media SET turn_id=?
WHERE episode_id=? AND session_id=? AND client_operation_id=? AND artifact_id=?`, turnID, episodeID.String(), sessionID, operationID, artifactID.String())
			if err != nil {
				return err
			}
			if count, _ := result.RowsAffected(); count != 1 {
				return sql.ErrNoRows
			}
		}
		return nil
	})
}

func artifactSelectColumns(alias string) string {
	return alias + `.artifact_id,` + alias + `.project_id,` + alias + `.media_kind,` + alias + `.relative_path,` + alias + `.sha256,` +
		alias + `.byte_size,` + alias + `.mime_type,` + alias + `.original_filename,` + alias + `.width,` + alias + `.height,` +
		alias + `.duration_ms,` + alias + `.fps_numerator,` + alias + `.fps_denominator,` + alias + `.audio_sample_rate,` +
		alias + `.audio_channels,` + alias + `.remote_reference_url,` + alias + `.remote_reference_expires_at_ms,` + alias + `.created_at_ms`
}
