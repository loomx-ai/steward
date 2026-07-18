package sqlite

import (
	"github.com/loomx-ai/steward/internal/persistence/relational"
	"gorm.io/gorm"
)

type Repositories struct {
	*relational.Store
}

func New(db *gorm.DB) *Repositories {
	return &Repositories{Store: relational.New(db)}
}
