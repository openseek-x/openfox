package conversations

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// A single row per viewer, agent and computer keeps both the size limit and ordering
// atomic when the same user creates two conversations at once.
func RememberWorkDirectory(ctx context.Context, tx pgx.Tx, org, user, agent, device, directory string) error {
	_, err := tx.Exec(ctx, `INSERT INTO openfox.conversation_work_directory_recents AS r
 (organization_id,user_id,agent_id,device_id,directories) VALUES ($1,$2,$3,$4,ARRAY[$5::text])
 ON CONFLICT (organization_id,user_id,agent_id,device_id) DO UPDATE
 SET directories=(ARRAY[$5::text] || array_remove(r.directories,$5::text))[1:10]`, org, user, agent, device, directory)
	return err
}

func recentWorkDirectories(ctx context.Context, tx pgx.Tx, org, user, agent, device string) ([]string, error) {

	var saved []string
	err := tx.QueryRow(ctx, `SELECT directories FROM openfox.conversation_work_directory_recents
 WHERE organization_id=$1 AND user_id=$2 AND agent_id=$3 AND device_id=$4`, org, user, agent, device).Scan(&saved)
	if err == pgx.ErrNoRows {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	return saved, nil
}

func ForgetWorkDirectory(ctx context.Context, tx pgx.Tx, org, user, agent, device, directory string) error {
	_, err := tx.Exec(ctx, `UPDATE openfox.conversation_work_directory_recents
 SET directories=array_remove(directories,$5::text)
 WHERE organization_id=$1 AND user_id=$2 AND agent_id=$3 AND device_id=$4 AND $5=ANY(directories)`, org, user, agent, device, directory)
	return err
}
