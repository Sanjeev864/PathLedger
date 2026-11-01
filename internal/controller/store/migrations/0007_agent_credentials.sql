-- Agent credential rotation: a new token is issued as "pending" and replaces
-- the current one the first time the agent uses it, so an agent that loses
-- the new token before saving it keeps working with the old one.
ALTER TABLE agents ADD COLUMN pending_token_hash bytea UNIQUE;
ALTER TABLE agents ADD COLUMN token_rotated_at timestamptz;
UPDATE agents SET token_rotated_at = created_at;
ALTER TABLE agents ALTER COLUMN token_rotated_at SET NOT NULL;
ALTER TABLE agents ALTER COLUMN token_rotated_at SET DEFAULT now();
