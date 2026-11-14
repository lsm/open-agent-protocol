#![allow(
    clippy::unwrap_used,
    clippy::expect_used,
    clippy::panic,
    clippy::indexing_slicing
)]

use std::path::PathBuf;

use oap_sdk::BinaryResolver;

static ENV_LOCK: std::sync::Mutex<()> = std::sync::Mutex::new(());

struct EnvGuard {
    keys: Vec<(&'static str, Option<String>)>,
    _lock: std::sync::MutexGuard<'static, ()>,
}

impl EnvGuard {
    fn set(pairs: &[(&'static str, Option<&str>)]) -> Self {
        let lock = ENV_LOCK
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner);
        let mut keys = Vec::new();
        for key in [
            "OXAP_SDK_BINARY_PATH",
            "OAP_SDK_BINARY_PATH",
            "OXAP_SDK_BINARY_URL",
            "OAP_SDK_BINARY_URL",
            "OXAP_SDK_BINARY_SHA256",
            "OAP_SDK_BINARY_SHA256",
            "PATH",
        ] {
            keys.push((key, std::env::var(key).ok()));
            std::env::remove_var(key);
        }
        for (key, value) in pairs {
            if let Some(value) = value {
                std::env::set_var(key, value);
            }
        }
        Self { keys, _lock: lock }
    }
}

impl Drop for EnvGuard {
    fn drop(&mut self) {
        for (key, value) in &self.keys {
            match value {
                Some(value) => std::env::set_var(key, value),
                None => std::env::remove_var(key),
            }
        }
    }
}

fn platform_name(name: &str) -> String {
    if cfg!(windows) {
        format!("{name}.exe")
    } else {
        name.to_owned()
    }
}

fn leaked(path: &std::path::Path) -> &'static str {
    Box::leak(path.to_string_lossy().into_owned().into_boxed_str())
}

const STUB_SHA256: &str = "306c6ca7407560340797866e077e053627ad409277d1b9da58106fce4cf717cb";
const OXAP_STUB_SHA256: &str = "56c24bb718ddf7067bbd57a16e43498dad3e645846bc7f4a0e5b408a425e3565";
const OAPX_STUB_SHA256: &str = "e51e04244dfdbdee83d5eee8d540d4c0d61aa22251bc5485fb5e5f804c24cb2a";

fn write_fake_binary(dir: &std::path::Path, relative: &[&str]) -> PathBuf {
    let mut path = dir.to_path_buf();
    for segment in relative {
        path.push(segment);
    }
    std::fs::create_dir_all(path.parent().expect("parent")).expect("mkdir");
    std::fs::write(&path, b"#!/bin/sh\nexit 0\n").expect("write");
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o755)).expect("chmod");
    }
    path
}

#[tokio::test]
async fn an_explicit_path_wins_over_local_builds() {
    let _guard = EnvGuard::set(&[]);
    let temp = tempfile::tempdir().expect("tempdir");
    let explicit = write_fake_binary(temp.path(), &["custom", "makai"]);
    write_fake_binary(temp.path(), &["zig-out", "bin", "oapx"]);

    let resolved = BinaryResolver {
        binary_path: Some(explicit.clone()),
        base_dir: Some(temp.path().to_path_buf()),
        ..Default::default()
    }
    .resolve()
    .await
    .expect("resolves");
    assert_eq!(resolved, explicit);
}

#[tokio::test]
async fn the_environment_variable_outranks_the_explicit_option() {
    let temp = tempfile::tempdir().expect("tempdir");
    let from_env = write_fake_binary(temp.path(), &["env", "makai"]);
    let from_option = write_fake_binary(temp.path(), &["option", "makai"]);
    let leaked = leaked(&from_env);
    let _guard = EnvGuard::set(&[("OAP_SDK_BINARY_PATH", Some(leaked))]);

    let resolved = BinaryResolver {
        binary_path: Some(from_option),
        ..Default::default()
    }
    .resolve()
    .await
    .expect("resolves");
    assert_eq!(resolved, PathBuf::from(leaked));
}

#[tokio::test]
async fn a_missing_explicit_path_is_an_error_not_a_fallback() {
    let _guard = EnvGuard::set(&[]);
    let temp = tempfile::tempdir().expect("tempdir");
    write_fake_binary(temp.path(), &["zig-out", "bin", "makai"]);

    let error = BinaryResolver {
        binary_path: Some(temp.path().join("nope").join("makai")),
        base_dir: Some(temp.path().to_path_buf()),
        ..Default::default()
    }
    .resolve()
    .await
    .unwrap_err();
    assert!(error.to_string().contains("not found"), "{error}");
}

#[tokio::test]
async fn local_builds_are_checked_in_order() {
    let _guard = EnvGuard::set(&[]);

    let temp = tempfile::tempdir().expect("tempdir");
    let nested = write_fake_binary(temp.path(), &["zig", "zig-out", "bin", "oapx"]);
    let resolved = BinaryResolver {
        base_dir: Some(temp.path().to_path_buf()),
        ..Default::default()
    }
    .resolve()
    .await
    .expect("resolves");
    assert_eq!(resolved, nested, "zig/zig-out is the second candidate");

    let top = write_fake_binary(temp.path(), &["zig-out", "bin", "oapx"]);
    let resolved = BinaryResolver {
        base_dir: Some(temp.path().to_path_buf()),
        ..Default::default()
    }
    .resolve()
    .await
    .expect("resolves");
    assert_eq!(resolved, top, "zig-out is the first candidate");
}

#[tokio::test]
async fn legacy_makai_does_not_shadow_oapx() {
    let _guard = EnvGuard::set(&[]);
    let temp = tempfile::tempdir().expect("tempdir");
    write_fake_binary(temp.path(), &["zig-out", "bin", "makai"]);
    let bin = temp.path().join("zig-out").join("bin");
    write_fake_binary(temp.path(), &["zig-out", "bin", "oapx"]);

    let resolved = BinaryResolver {
        base_dir: Some(temp.path().to_path_buf()),
        ..Default::default()
    }
    .resolve()
    .await
    .expect("resolves");
    assert_eq!(resolved, bin.join("oapx"));
}

#[tokio::test]
async fn a_directory_named_like_the_binary_does_not_shadow_a_usable_one() {
    let _guard = EnvGuard::set(&[]);
    let temp = tempfile::tempdir().expect("tempdir");
    let top = temp.path().join("zig-out").join("bin");
    let nested = temp.path().join("zig").join("zig-out").join("bin");
    std::fs::create_dir_all(&top).expect("create top");
    std::fs::create_dir_all(top.join("oapx")).expect("decoy directory");
    std::fs::create_dir_all(&nested).expect("create nested");
    let real = write_fake_binary(temp.path(), &["zig", "zig-out", "bin", "oapx"]);

    let resolved = BinaryResolver {
        base_dir: Some(temp.path().to_path_buf()),
        ..Default::default()
    }
    .resolve()
    .await
    .expect("resolves");
    assert_eq!(resolved, real);
}

#[tokio::test]
async fn an_install_predating_the_rename_is_not_resolved() {
    let _guard = EnvGuard::set(&[]);
    let temp = tempfile::tempdir().expect("tempdir");
    write_fake_binary(temp.path(), &["zig-out", "bin", "makai"]);

    let resolved = BinaryResolver {
        base_dir: Some(temp.path().to_path_buf()),
        ..Default::default()
    }
    .resolve()
    .await
    .expect("resolves");
    assert_eq!(resolved, PathBuf::from(platform_name("oxap")));
}

#[tokio::test]
async fn with_nothing_to_find_the_bare_command_is_returned_for_path_lookup() {
    let _guard = EnvGuard::set(&[]);
    let temp = tempfile::tempdir().expect("tempdir");

    let resolved = BinaryResolver {
        base_dir: Some(temp.path().to_path_buf()),
        ..Default::default()
    }
    .resolve()
    .await
    .expect("resolves");
    assert_eq!(resolved, PathBuf::from(platform_name("oxap")));
}

#[tokio::test]
async fn the_oxap_environment_path_outranks_the_legacy_one() {
    let temp = tempfile::tempdir().expect("tempdir");
    let preferred = write_fake_binary(temp.path(), &["env", "from-oxap"]);
    let legacy = write_fake_binary(temp.path(), &["env", "from-oap"]);
    let _guard = EnvGuard::set(&[
        ("OAP_SDK_BINARY_PATH", Some(leaked(&legacy))),
        ("OXAP_SDK_BINARY_PATH", Some(leaked(&preferred))),
    ]);

    let resolved = BinaryResolver::default().resolve().await.expect("resolves");
    assert_eq!(resolved, preferred);
}

#[tokio::test]
async fn the_legacy_environment_path_still_resolves() {
    let temp = tempfile::tempdir().expect("tempdir");
    let legacy = write_fake_binary(temp.path(), &["env", "from-oap"]);
    let _guard = EnvGuard::set(&[("OAP_SDK_BINARY_PATH", Some(leaked(&legacy)))]);

    let resolved = BinaryResolver::default().resolve().await.expect("resolves");
    assert_eq!(resolved, legacy);
}

#[tokio::test]
async fn a_local_oxap_build_outranks_a_local_oapx_build() {
    let _guard = EnvGuard::set(&[]);
    let temp = tempfile::tempdir().expect("tempdir");
    write_fake_binary(temp.path(), &["zig-out", "bin", &platform_name("oapx")]);
    let preferred = write_fake_binary(
        temp.path(),
        &["zig", "zig-out", "bin", &platform_name("oxap")],
    );

    let resolved = BinaryResolver {
        base_dir: Some(temp.path().to_path_buf()),
        ..Default::default()
    }
    .resolve()
    .await
    .expect("resolves");
    assert_eq!(
        resolved, preferred,
        "a nested oxap build outranks a top-level oapx one"
    );
}

#[tokio::test]
async fn a_local_oapx_build_still_resolves() {
    let _guard = EnvGuard::set(&[]);
    let temp = tempfile::tempdir().expect("tempdir");
    let legacy = write_fake_binary(temp.path(), &["zig-out", "bin", &platform_name("oapx")]);

    let resolved = BinaryResolver {
        base_dir: Some(temp.path().to_path_buf()),
        ..Default::default()
    }
    .resolve()
    .await
    .expect("resolves");
    assert_eq!(resolved, legacy);
}

#[tokio::test]
async fn oxap_on_path_outranks_oapx_on_path() {
    let temp = tempfile::tempdir().expect("tempdir");
    let bin = temp.path().join("bin");
    write_fake_binary(&bin, &[&platform_name("oapx")]);
    let preferred = write_fake_binary(&bin, &[&platform_name("oxap")]);
    let _guard = EnvGuard::set(&[("PATH", Some(leaked(&bin)))]);

    let resolved = BinaryResolver {
        base_dir: Some(temp.path().join("empty")),
        ..Default::default()
    }
    .resolve()
    .await
    .expect("resolves");
    assert_eq!(resolved, preferred);
}

#[tokio::test]
async fn oapx_on_path_still_resolves() {
    let temp = tempfile::tempdir().expect("tempdir");
    let bin = temp.path().join("bin");
    let legacy = write_fake_binary(&bin, &[&platform_name("oapx")]);
    let _guard = EnvGuard::set(&[("PATH", Some(leaked(&bin)))]);

    let resolved = BinaryResolver {
        base_dir: Some(temp.path().join("empty")),
        ..Default::default()
    }
    .resolve()
    .await
    .expect("resolves");
    assert_eq!(resolved, legacy);
}

#[tokio::test]
async fn a_url_without_a_checksum_is_rejected() {
    let _guard = EnvGuard::set(&[]);
    let error = BinaryResolver {
        binary_url: Some("https://example.invalid/makai".to_owned()),
        ..Default::default()
    }
    .resolve()
    .await
    .unwrap_err();
    assert!(
        error.to_string().contains("SHA-256 checksum is required"),
        "{error}"
    );
}

#[tokio::test]
async fn the_oxap_url_outranks_the_legacy_url() {
    let temp = tempfile::tempdir().expect("tempdir");
    let cache = temp.path().join("cache");
    std::fs::create_dir_all(&cache).expect("mkdir");
    let oxap = cache.join("oxap-linux-arm64");
    let legacy = cache.join("oapx-linux-arm64");
    std::fs::write(&oxap, b"#!/bin/sh\necho oxap\n").expect("write");
    std::fs::write(&legacy, b"#!/bin/sh\necho oapx\n").expect("write");
    let _guard = EnvGuard::set(&[
        (
            "OAP_SDK_BINARY_URL",
            Some("https://example.invalid/releases/oapx-linux-arm64"),
        ),
        ("OAP_SDK_BINARY_SHA256", Some(OAPX_STUB_SHA256)),
        (
            "OXAP_SDK_BINARY_URL",
            Some("https://example.invalid/releases/oxap-linux-arm64"),
        ),
        ("OXAP_SDK_BINARY_SHA256", Some(OXAP_STUB_SHA256)),
    ]);

    let resolved = BinaryResolver {
        cache_dir: Some(cache),
        ..Default::default()
    }
    .resolve()
    .await
    .expect("the OXAP pair resolves first");
    assert_eq!(resolved, oxap);
}

#[tokio::test]
async fn the_legacy_url_still_resolves() {
    let temp = tempfile::tempdir().expect("tempdir");
    let cache = temp.path().join("cache");
    std::fs::create_dir_all(&cache).expect("mkdir");
    let bytes = b"#!/bin/sh\nexit 0\n";
    let legacy = cache.join("oapx-linux-arm64");
    std::fs::write(&legacy, bytes).expect("write");
    let _guard = EnvGuard::set(&[
        (
            "OAP_SDK_BINARY_URL",
            Some("https://example.invalid/releases/oapx-linux-arm64"),
        ),
        ("OAP_SDK_BINARY_SHA256", Some(STUB_SHA256)),
    ]);

    let resolved = BinaryResolver {
        cache_dir: Some(cache),
        ..Default::default()
    }
    .resolve()
    .await
    .expect("the cached legacy URL entry resolves");
    assert_eq!(resolved, legacy);
}

#[tokio::test]
async fn a_checksum_is_not_borrowed_across_the_two_url_pairs() {
    let temp = tempfile::tempdir().expect("tempdir");
    let cache = temp.path().join("cache");
    std::fs::create_dir_all(&cache).expect("mkdir");
    let bytes = b"#!/bin/sh\nexit 0\n";
    std::fs::write(cache.join("oxap-linux-arm64"), bytes).expect("write");
    let _guard = EnvGuard::set(&[
        (
            "OXAP_SDK_BINARY_URL",
            Some("https://example.invalid/releases/oxap-linux-arm64"),
        ),
        ("OAP_SDK_BINARY_SHA256", Some(STUB_SHA256)),
    ]);

    let error = BinaryResolver {
        cache_dir: Some(cache),
        ..Default::default()
    }
    .resolve()
    .await
    .unwrap_err();
    assert!(
        error.to_string().contains("SHA-256 checksum is required"),
        "the legacy checksum must not satisfy the OXAP URL: {error}"
    );
}

#[tokio::test]
async fn a_cached_download_is_used_when_its_checksum_matches() {
    let _guard = EnvGuard::set(&[]);
    let temp = tempfile::tempdir().expect("tempdir");
    let cache = temp.path().join("cache");
    std::fs::create_dir_all(&cache).expect("mkdir");
    std::fs::write(cache.join("makai-darwin-arm64"), b"makai").expect("write");

    let resolved = BinaryResolver {
        binary_url: Some("https://example.invalid/releases/makai-darwin-arm64".to_owned()),
        checksum_sha256: Some(
            "439a79bbe49c475f4732c0cf16ae14793df79123bfe09e53b1ed32e6d9f4d13c".to_owned(),
        ),
        cache_dir: Some(cache.clone()),
        ..Default::default()
    }
    .resolve()
    .await
    .expect("the cached file is verified and used");
    assert_eq!(resolved, cache.join("makai-darwin-arm64"));
}

#[tokio::test]
async fn the_builder_command_bypasses_the_environment_override() {
    let _guard = EnvGuard::set(&[("OAP_SDK_BINARY_PATH", Some("/definitely/not/here/makai"))]);

    let client = oap_sdk::ClientBuilder::new()
        .command(env!("CARGO_BIN_EXE_oap-protocol-fake"))
        .args(Vec::<String>::new())
        .env_clear()
        .env("OAP_SDK_FAKE_SCENARIO", "ok")
        .handshake_timeout(std::time::Duration::from_millis(2_000))
        .connect()
        .await
        .expect("the pinned command is used");
    client.close().await;
}

#[tokio::test]
async fn a_cached_download_with_the_wrong_checksum_is_discarded() {
    let _guard = EnvGuard::set(&[]);
    let temp = tempfile::tempdir().expect("tempdir");
    let cache = temp.path().join("cache");
    std::fs::create_dir_all(&cache).expect("mkdir");
    let cached = cache.join("makai");
    std::fs::write(&cached, b"tampered").expect("write");

    let result = BinaryResolver {
        binary_url: Some("https://example.invalid/makai".to_owned()),
        checksum_sha256: Some(
            "439a79bbe49c475f4732c0cf16ae14793df79123bfe09e53b1ed32e6d9f4d13c".to_owned(),
        ),
        cache_dir: Some(cache),
        ..Default::default()
    }
    .resolve()
    .await;

    assert!(result.is_err(), "a tampered cache must not be used");
    assert!(!cached.exists(), "the rejected file is removed");
}

#[cfg(not(feature = "download"))]
#[tokio::test]
async fn without_the_download_feature_a_cache_miss_is_a_clear_error() {
    let _guard = EnvGuard::set(&[]);
    let temp = tempfile::tempdir().expect("tempdir");

    let error = BinaryResolver {
        binary_url: Some("https://example.invalid/makai".to_owned()),
        checksum_sha256: Some("0".repeat(64)),
        cache_dir: Some(temp.path().join("cache")),
        ..Default::default()
    }
    .resolve()
    .await
    .unwrap_err();
    assert!(error.to_string().contains("`download` feature"), "{error}");
}
