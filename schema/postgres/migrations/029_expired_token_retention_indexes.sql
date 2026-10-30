-- +goose Up
-- The earlier partial indexes exclude consumed and revoked rows, but those rows
-- are part of the retention set too. Full expiry indexes keep the daily deletes
-- bounded as the token tables grow.
drop index refresh_tokens_expires_at_idx;
create index refresh_tokens_expires_at_idx
  on refresh_tokens (expires_at);

drop index email_action_tokens_expires_at_idx;
create index email_action_tokens_expires_at_idx
  on email_action_tokens (expires_at);

-- +goose Down
drop index refresh_tokens_expires_at_idx;
create index refresh_tokens_expires_at_idx
  on refresh_tokens (expires_at)
  where consumed_at is null and revoked_at is null;

drop index email_action_tokens_expires_at_idx;
create index email_action_tokens_expires_at_idx
  on email_action_tokens (expires_at)
  where consumed_at is null;
