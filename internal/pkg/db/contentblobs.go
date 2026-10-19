package db

import (
	"context"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/contentblob"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	// maximumContentBlobRequest bounds how many blobs one put or get names.
	maximumContentBlobRequest = 4096

	// contentBlobLookupChunk keeps one lookup under SQLite's bound-variable
	// limit when a page of rows refers to many blobs.
	contentBlobLookupChunk = 1000

	contentBlobExpansionCallback = "peen:expand_content_blobs"
)

// ContentBlobStore keeps the long strings of compacted durable state in the
// session database. See package contentblob.
type ContentBlobStore struct {
	gormDB *gorm.DB
}

var _ contentblob.Store = (*ContentBlobStore)(nil)

// ContentBlobs returns the blob store of this database.
func (h *Handle) ContentBlobs() *ContentBlobStore {
	return &ContentBlobStore{gormDB: h.GormDB}
}

// PutContentBlobs stores blobs. Storing a blob that is already stored is a
// no-op. Every blob must be one string literal whose bytes match its hash, so
// no caller can store text under a hash it does not own.
func (s *ContentBlobStore) PutContentBlobs(
	ctx context.Context,
	blobs []contentblob.Blob,
) error {
	if len(blobs) > maximumContentBlobRequest {
		return ctxerrors.Wrapf(commerr.ErrValidationFailed,
			"at most %d content blobs per request", maximumContentBlobRequest)
	}

	if len(blobs) == 0 {
		return nil
	}

	rows := make([]*models.ContentBlob, 0, len(blobs))
	for _, blob := range blobs {
		if !contentblob.ValidBlob(blob.Hash, blob.Data) {
			return ctxerrors.Wrapf(commerr.ErrValidationFailed,
				"content blob %q is not a string literal matching its hash",
				blob.Hash)
		}

		rows = append(rows, &models.ContentBlob{
			Hash: blob.Hash, Data: blob.Data,
		})
	}

	if err := repositories.Use(s.gormDB).ContentBlob.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(rows...); err != nil {
		return ctxerrors.Wrap(err, "store content blobs")
	}

	return nil
}

// GetContentBlobs returns the stored blobs for the requested hashes. A hash
// that is not stored is left out.
func (s *ContentBlobStore) GetContentBlobs(
	ctx context.Context,
	hashes []string,
) (map[string][]byte, error) {
	if len(hashes) > maximumContentBlobRequest {
		return nil, ctxerrors.Wrapf(commerr.ErrValidationFailed,
			"at most %d content blobs per request", maximumContentBlobRequest)
	}

	return findContentBlobs(ctx, s.gormDB, hashes)
}

func findContentBlobs(
	ctx context.Context,
	gormDB *gorm.DB,
	hashes []string,
) (map[string][]byte, error) {
	found := make(map[string][]byte, len(hashes))
	repository := repositories.Use(gormDB).ContentBlob

	for start := 0; start < len(hashes); start += contentBlobLookupChunk {
		chunk := hashes[start:min(start+contentBlobLookupChunk, len(hashes))]

		rows, err := repository.WithContext(ctx).
			Where(repository.Hash.In(chunk...)).
			Find()
		if err != nil {
			return nil, ctxerrors.Wrap(err, "find content blobs")
		}

		for _, row := range rows {
			found[row.Hash] = row.Data
		}
	}

	return found, nil
}

// installContentBlobExpansion restores compacted columns after every query
// that loads executions or execution activities, so every reader sees the
// documents that were saved. The session store compacts them on write.
func installContentBlobExpansion(database *gorm.DB) error {
	expander := &contentExpander{
		cache:     contentblob.NewCache(contentblob.DefaultCacheBytes),
		documents: contentblob.NewCache(contentblob.DefaultCacheBytes),
	}

	err := database.Callback().Query().After("gorm:query").
		Register(contentBlobExpansionCallback, expander.expand)
	if err != nil {
		return ctxerrors.Wrap(err, "register content blob expansion")
	}

	return nil
}

// contentExpander restores compacted columns after queries. Its caches live
// as long as the database handle: executions are loaded again and again, and
// their blobs never change. A compacted document names its content exactly,
// so documents keeps whole restored documents under their compacted text.
type contentExpander struct {
	cache     *contentblob.Cache
	documents *contentblob.Cache
}

func (e *contentExpander) expand(tx *gorm.DB) {
	if tx.Error != nil || tx.Statement == nil {
		return
	}

	columns := compactedColumns(tx.Statement.Dest)
	if len(columns) == 0 {
		return
	}

	// A new session on the same connection pool reads inside the caller's
	// transaction. NewDB only takes effect on the next chain call, and the
	// repositories wrap the handle in another session first, which would copy
	// this query's table and conditions. Model makes the clean statement now.
	fresh := tx.Session(&gorm.Session{NewDB: true}).
		Model(&models.ContentBlob{})
	ctx := tx.Statement.Context

	fetch := contentblob.CachedFetch(e.cache,
		func(hashes []string) (map[string][]byte, error) {
			return findContentBlobs(ctx, fresh, hashes)
		})

	for _, column := range columns {
		if expanded, ok := e.documents.Get(*column); ok {
			*column = string(expanded)

			continue
		}

		expanded, err := contentblob.Expand([]byte(*column), fetch)
		if err != nil {
			_ = tx.AddError(ctxerrors.Wrap(err, "expand stored document"))

			return
		}

		e.documents.Add(*column, expanded)
		*column = string(expanded)
	}
}

// compactedColumns returns the compacted JSON columns of whatever a query
// loaded into dest. The generated repositories load one record into a pointer
// and a list into a pointer to a slice of pointers.
func compactedColumns(dest any) []*string {
	if columns, ok := loadedActivityColumns(dest); ok {
		return columns
	}

	return loadedExecutionColumns(dest)
}

func loadedActivityColumns(dest any) ([]*string, bool) {
	var columns []*string

	switch loaded := dest.(type) {
	case *models.ExecutionActivity:
		columns = activityColumns(columns, loaded)
	case *[]*models.ExecutionActivity:
		for _, activity := range *loaded {
			columns = activityColumns(columns, activity)
		}
	case *[]models.ExecutionActivity:
		for index := range *loaded {
			columns = activityColumns(columns, &(*loaded)[index])
		}
	default:
		return nil, false
	}

	return columns, true
}

func loadedExecutionColumns(dest any) []*string {
	var columns []*string

	switch loaded := dest.(type) {
	case *models.Execution:
		columns = executionColumns(columns, loaded)
	case *[]*models.Execution:
		for _, execution := range *loaded {
			columns = executionColumns(columns, execution)
		}
	case *[]models.Execution:
		for index := range *loaded {
			columns = executionColumns(columns, &(*loaded)[index])
		}
	}

	return columns
}

func activityColumns(
	columns []*string,
	activity *models.ExecutionActivity,
) []*string {
	if activity == nil {
		return columns
	}

	columns = appendCompacted(columns, &activity.InputJSON)
	if activity.OutputJSON != nil {
		columns = appendCompacted(columns, activity.OutputJSON)
	}

	return appendCompacted(columns, &activity.ContextJSON)
}

func executionColumns(
	columns []*string,
	execution *models.Execution,
) []*string {
	if execution == nil {
		return columns
	}

	return appendCompacted(columns, &execution.ContextJSON)
}

func appendCompacted(columns []*string, column *string) []*string {
	if contentblob.IsCompacted([]byte(*column)) {
		return append(columns, column)
	}

	return columns
}
