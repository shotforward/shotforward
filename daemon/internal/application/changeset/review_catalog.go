package changesetapp

import (
	"context"
	"errors"

	changeset "github.com/shotforward/shotforward/daemon/internal/domain/changeset"
)

// ReviewCatalog adapts globally unique ChangeSet routes to the project-scoped
// Service. It remains a local UI capability and is never passed to MCP.
type ReviewCatalog struct {
	service *Service
	locator Locator
}

func NewReviewCatalog(service *Service, locator Locator) (*ReviewCatalog, error) {
	if service == nil || locator == nil {
		return nil, errors.New("create ChangeSet review catalog: dependencies are required")
	}
	return &ReviewCatalog{service: service, locator: locator}, nil
}

func (c *ReviewCatalog) Get(ctx context.Context, id changeset.ChangeSetID) (changeset.ChangeSet, error) {
	projectID, err := c.locator.LocateChangeSet(ctx, id)
	if err != nil {
		return changeset.ChangeSet{}, err
	}
	return c.service.Get(ctx, projectID, id)
}

func (c *ReviewCatalog) Operations(ctx context.Context, id changeset.ChangeSetID, afterPosition, limit int) ([]changeset.Operation, error) {
	projectID, err := c.locator.LocateChangeSet(ctx, id)
	if err != nil {
		return nil, err
	}
	return c.service.Operations(ctx, projectID, id, afterPosition, limit)
}

func (c *ReviewCatalog) Apply(ctx context.Context, id changeset.ChangeSetID, expectedRevision int64, operationKey string) (CommandResult, error) {
	projectID, err := c.locator.LocateChangeSet(ctx, id)
	if err != nil {
		return CommandResult{}, err
	}
	return c.service.Apply(ctx, ApplyInput{ProjectID: projectID, ChangeSetID: id, ExpectedRevision: expectedRevision, OperationKey: operationKey})
}

func (c *ReviewCatalog) Reject(ctx context.Context, id changeset.ChangeSetID, expectedRevision int64, reason, operationKey string) (CommandResult, error) {
	projectID, err := c.locator.LocateChangeSet(ctx, id)
	if err != nil {
		return CommandResult{}, err
	}
	return c.service.Reject(ctx, RejectInput{ProjectID: projectID, ChangeSetID: id, ExpectedRevision: expectedRevision, Reason: reason, OperationKey: operationKey})
}

func (c *ReviewCatalog) Undo(ctx context.Context, id changeset.ChangeSetID, operationKey string) (CommandResult, error) {
	projectID, err := c.locator.LocateChangeSet(ctx, id)
	if err != nil {
		return CommandResult{}, err
	}
	return c.service.Undo(ctx, UndoInput{ProjectID: projectID, ChangeSetID: id, OperationKey: operationKey})
}
