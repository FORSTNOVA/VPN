//! Runs the production kernel with an isolated TUN configuration for windows_quic_tun.py.
use anyhow::{Context, Result};
use clash_lib::{Config, Options};
use std::{path::PathBuf, thread, time::{Duration, Instant}};

fn main() -> Result<()> {
    let dir = PathBuf::from(std::env::args_os().nth(1).context("expected fixture directory")?);
    let config = Config::Str(std::fs::read_to_string(dir.join("kernel.json"))?).try_parse_strict()?;
    let stop = dir.join("stop");
    let worker = thread::spawn(move || clash_lib::start_scaffold(Options {
        config: Config::Internal(config), cwd: Some(dir.to_string_lossy().into_owned()),
        rt: None, log_file: None, config_path: None,
    }));
    let deadline = Instant::now() + Duration::from_secs(100);
    while !worker.is_finished() && !stop.exists() && Instant::now() < deadline {
        thread::sleep(Duration::from_millis(100));
    }
    while !worker.is_finished() && !clash_lib::shutdown() {
        thread::sleep(Duration::from_millis(10));
    }
    worker.join().expect("kernel worker panicked")?;
    Ok(())
}
