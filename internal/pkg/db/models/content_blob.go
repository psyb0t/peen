package models

// ContentBlob stores one long JSON string literal of durable execution state,
// once, under the SHA-256 of its bytes. Compacted documents refer to it by
// hash. See package contentblob.
//
// The table is shared by every session. A workflow converter encodes payloads
// without knowing their session, and a blob reveals nothing to a reader who
// does not already hold its exact bytes, because the hash is the only key.
type ContentBlob struct {
	Hash string `gorm:"primaryKey"`
	Data []byte
}

// TableName pins the schema-owned content_blobs table name.
func (ContentBlob) TableName() string {
	return "content_blobs"
}
