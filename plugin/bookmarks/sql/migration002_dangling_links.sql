-- ONCE
-- Deleting a folder once kept its bookmarks by leaving their links to the
-- folder in place, so they belonged to a folder that no longer existed and
-- appeared nowhere in the tree. Such a bookmark moves to the root folder.
-- bookmark_folders.sort_order also changes meaning, from the bookmark's index
-- among its folders to its place in the folder, so it restarts from the id.
INSERT OR IGNORE INTO bookmark_folders (bookmark_id, folder_id)
SELECT bookmark_id, -1 FROM bookmark_folders
WHERE folder_id != -1 AND folder_id NOT IN (SELECT id FROM folders);
DELETE FROM bookmark_folders WHERE folder_id != -1 AND folder_id NOT IN (SELECT id FROM folders);
UPDATE bookmark_folders SET sort_order = bookmark_id;
