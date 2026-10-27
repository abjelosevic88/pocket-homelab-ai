-- Native notification choices belong to a registered device, never web settings.
ALTER TABLE mobile_devices ADD COLUMN notify_finished INTEGER NOT NULL DEFAULT 1;
ALTER TABLE mobile_devices ADD COLUMN notify_input INTEGER NOT NULL DEFAULT 1;
ALTER TABLE mobile_devices ADD COLUMN notify_failed INTEGER NOT NULL DEFAULT 1;
ALTER TABLE mobile_devices ADD COLUMN notify_only_away INTEGER NOT NULL DEFAULT 1;
-- Snapshot the old policy once for existing devices. Later web edits stay separate.
UPDATE mobile_devices SET
 notify_finished=COALESCE((SELECT value != '0' FROM user_settings WHERE user_id=mobile_devices.user_id AND key='notify_finished'),1),
 notify_input=COALESCE((SELECT value != '0' FROM user_settings WHERE user_id=mobile_devices.user_id AND key='notify_input'),1),
 notify_failed=COALESCE((SELECT value != '0' FROM user_settings WHERE user_id=mobile_devices.user_id AND key='notify_failed'),1),
 notify_only_away=COALESCE((SELECT value != '0' FROM user_settings WHERE user_id=mobile_devices.user_id AND key='notify_only_away'),1);
