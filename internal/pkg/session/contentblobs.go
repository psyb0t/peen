package session

import (
	"context"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/internal/pkg/contentblob"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
	"gorm.io/gorm/clause"
)

var _ contentblob.Store = (*Store)(nil)

// PutContentBlobs stores the blobs of a compacted workflow payload.
func (s *Store) PutContentBlobs(
	ctx context.Context,
	blobs []contentblob.Blob,
) error {
	if err := s.blobs.PutContentBlobs(ctx, blobs); err != nil {
		return ctxerrors.Wrap(err, "put content blobs")
	}

	return nil
}

// GetContentBlobs reads stored blobs by hash.
func (s *Store) GetContentBlobs(
	ctx context.Context,
	hashes []string,
) (map[string][]byte, error) {
	found, err := s.blobs.GetContentBlobs(ctx, hashes)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "get content blobs")
	}

	return found, nil
}

// compactColumn stores raw's long strings as blobs inside the caller's
// transaction and returns the document to write in raw's place. Readers get
// raw back through the models' AfterFind hooks.
func compactColumn(
	ctx context.Context,
	query *repositories.Query,
	raw string,
) (string, error) {
	compacted, blobs := contentblob.Compact([]byte(raw))
	if len(blobs) == 0 {
		return raw, nil
	}

	rows := make([]*models.ContentBlob, 0, len(blobs))
	for _, blob := range blobs {
		rows = append(rows, &models.ContentBlob{
			Hash: blob.Hash, Data: blob.Data,
		})
	}

	if err := query.ContentBlob.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(rows...); err != nil {
		return "", ctxerrors.Wrap(err, "store content blobs")
	}

	return string(compacted), nil
}
