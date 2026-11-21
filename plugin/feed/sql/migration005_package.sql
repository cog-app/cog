-- A download is packaged for HLS: local_path becomes its audio track and
-- video_path its video track, each a fragmented MP4 with a sidx
-- (pipeline/package.go). package_state is '' until the packager has looked
-- at it, then 'done' or 'error'.
-- IF COLUMN downloads.video_path NOT EXIST
ALTER TABLE downloads ADD COLUMN video_path TEXT NOT NULL DEFAULT '';
ALTER TABLE downloads ADD COLUMN package_state TEXT NOT NULL DEFAULT '';
ALTER TABLE downloads ADD COLUMN package_error TEXT NOT NULL DEFAULT '';
