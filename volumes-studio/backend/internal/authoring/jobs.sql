-- Fences a canceled/reassigned validation attempt even if its old goroutine is
-- still hashing or waiting for central principal authority.
ALTER TABLE versions ADD COLUMN generation INTEGER NOT NULL DEFAULT 0;
