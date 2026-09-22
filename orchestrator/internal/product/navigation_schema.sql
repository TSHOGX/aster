-- Version 7: final-answer lookup must not scan unrelated Session messages for
-- every turn in a bounded navigation page or dirty-turn projection batch.
CREATE INDEX messages_turn_final ON messages(session_id,turn_id,sequence DESC) WHERE role='assistant';
