-- name: DeleteExpiredRefreshTokens :execrows
-- Used and revoked rows too, but never before expires_at: a used row is what
-- catches a replayed token.
delete from refresh_tokens
where expires_at <= now();

-- name: DeleteExpiredEmailActionTokens :execrows
-- Used rows too, but never before expires_at: ResendInvite's cap counts them.
delete from email_action_tokens
where expires_at <= now();
