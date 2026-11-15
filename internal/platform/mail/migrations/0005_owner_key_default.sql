-- A deployment that has never thought about the owner key should still get one.
--
-- Recognition used to fall back to the address alone when the key was empty,
-- which is exactly the hole the key exists to close. Now every row carries a
-- key from the moment it exists: a new row gets one from the column default,
-- and the one row that predates this migration gets one here. The value is
-- readable in the console, so the owner can type it into the prompt; nothing
-- needs it to be memorable.
ALTER TABLE mail_settings
	ALTER COLUMN owner_key SET DEFAULT replace(gen_random_uuid()::text || gen_random_uuid()::text, '-', '');

UPDATE mail_settings
	SET owner_key = replace(gen_random_uuid()::text || gen_random_uuid()::text, '-', '')
	WHERE owner_key = '';
