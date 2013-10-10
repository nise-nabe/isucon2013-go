ALTER TABLE `memos` ADD INDEX `i1` (`is_private`, `created_at`);
ALTER TABLE `memos` ADD INDEX `i2` (`user`, `is_private`, `created_at`);
ALTER TABLE `memos` ADD INDEX `i3` (`user`, `created_at`);
