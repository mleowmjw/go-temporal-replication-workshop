package bluegreen_test

import (
	"testing"
	"time"

	"app/internal/bluegreen"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validPlan() bluegreen.MigrationPlan {
	return bluegreen.MigrationPlan{
		ID:          "deploy-rename-full-name",
		Description: "Rename full_name to display_name and add phone column",
		ExpandSQL: []string{
			"ALTER TABLE inventory.customers ADD COLUMN display_name TEXT",
			"UPDATE inventory.customers SET display_name = full_name WHERE display_name IS NULL",
			"ALTER TABLE inventory.customers ADD COLUMN phone TEXT",
		},
		ContractSQL: []string{
			"ALTER TABLE inventory.customers DROP COLUMN full_name",
		},
		RollbackSQL: []string{
			"ALTER TABLE inventory.customers DROP COLUMN IF EXISTS display_name",
			"ALTER TABLE inventory.customers DROP COLUMN IF EXISTS phone",
		},
		VerifyQueries: []bluegreen.VerifyQuery{
			{
				Name:      "all_display_names_backfilled",
				SQL:       "SELECT count(*) FROM inventory.customers WHERE display_name IS NULL AND full_name IS NOT NULL",
				WantCount: 0,
			},
		},
	}
}

func TestValidateMigrationPlan_Valid(t *testing.T) {
	require.NoError(t, bluegreen.ValidateMigrationPlan(validPlan()))
}

func TestValidateMigrationPlan_MissingID(t *testing.T) {
	p := validPlan()
	p.ID = ""
	err := bluegreen.ValidateMigrationPlan(p)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ID")
}

func TestValidateMigrationPlan_MissingExpandSQL(t *testing.T) {
	p := validPlan()
	p.ExpandSQL = nil
	err := bluegreen.ValidateMigrationPlan(p)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expand")
}

func TestValidateMigrationPlan_MissingContractSQL(t *testing.T) {
	p := validPlan()
	p.ContractSQL = nil
	err := bluegreen.ValidateMigrationPlan(p)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "contract")
}

func TestValidateMigrationPlan_MissingRollbackSQL(t *testing.T) {
	p := validPlan()
	p.RollbackSQL = nil
	err := bluegreen.ValidateMigrationPlan(p)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rollback")
}

func TestValidateMigrationPlan_EmptyVerifyQuerySQL(t *testing.T) {
	p := validPlan()
	p.VerifyQueries = []bluegreen.VerifyQuery{{Name: "check", SQL: ""}}
	err := bluegreen.ValidateMigrationPlan(p)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "verify query")
}

func TestEffectiveReadOnlyDuration_Default(t *testing.T) {
	p := validPlan()
	assert.Equal(t, bluegreen.DefaultReadOnlyMaxDuration, p.EffectiveReadOnlyDuration())
}

func TestEffectiveReadOnlyDuration_Override(t *testing.T) {
	p := validPlan()
	p.ReadOnlyMaxDuration = 2 * time.Minute
	assert.Equal(t, 2*time.Minute, p.EffectiveReadOnlyDuration())
}
