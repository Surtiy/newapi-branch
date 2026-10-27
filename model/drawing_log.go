package model

import (
	"errors"
	"strings"

	"gorm.io/gorm"
)

// SaveDrawingLog shares the drawing-history index with Midjourney without
// creating a provider task or participating in its polling/billing lifecycle.
// Only terminal relay-image rows may use this path.
func SaveDrawingLog(record *Midjourney) error {
	if record == nil || record.UserId <= 0 || !strings.HasPrefix(record.MjId, "img_") || record.Progress != "100%" {
		return errors.New("invalid terminal drawing log")
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		// The existing owner row serializes identical request projections across
		// nodes without adding a uniqueness constraint to legacy MJ task IDs.
		var owner User
		if err := lockForUpdate(tx).Select("id").Where("id = ?", record.UserId).Take(&owner).Error; err != nil {
			return err
		}
		var previous Midjourney
		err := tx.Where("user_id = ? AND mj_id = ?", record.UserId, record.MjId).Take(&previous).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return tx.Create(record).Error
		}
		if err != nil {
			return err
		}
		record.Id = previous.Id
		return tx.Model(&previous).Select("*").Updates(record).Error
	})
}

func GetDrawingLog(id, userID int, administrator bool) (*Midjourney, error) {
	query := DB.Where("id = ?", id)
	if !administrator {
		query = query.Where("user_id = ?", userID)
	}
	var record Midjourney
	err := query.Take(&record).Error
	return &record, err
}
