use std::io::Write;
use std::path::PathBuf;

use super::write_patch;

#[test]
fn a_patch_whose_write_fails_halfway_leaves_no_file_behind() {
    let mut written = PathBuf::new();
    let failed = write_patch("- id: compaction-basic\n", |path, file, bytes| {
        written = path.to_path_buf();
        file.write_all(&bytes[..bytes.len() / 2])?;
        file.flush()?;
        assert!(path.exists());
        Err(std::io::Error::other("disk full"))
    });
    assert_eq!(failed.unwrap_err().to_string(), "disk full");
    assert!(!written.as_os_str().is_empty());
    assert!(!written.exists());
}
