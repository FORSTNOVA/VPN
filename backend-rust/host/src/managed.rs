use std::{
    fs::OpenOptions,
    io::{self, Read},
    os::windows::{
        io::{AsRawHandle, FromRawHandle, OwnedHandle},
        process::CommandExt,
    },
    path::Path,
    process::{self, Command, Stdio},
    sync::{mpsc, OnceLock},
    thread,
    time::Duration,
};
use windows_sys::Win32::System::{
    JobObjects::{
        AssignProcessToJobObject, CreateJobObjectW, JobObjectExtendedLimitInformation,
        SetInformationJobObject, JOBOBJECT_EXTENDED_LIMIT_INFORMATION,
        JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
    },
    Threading::{GetCurrentProcess, CREATE_NO_WINDOW},
};

// Join our own job before spawning. The child inherits membership at creation;
// even a supervisor killed between spawn and PID publication leaves no orphan.
fn lifetime_job() -> io::Result<OwnedHandle> {
    unsafe {
        let raw = CreateJobObjectW(std::ptr::null(), std::ptr::null());
        if raw.is_null() {
            return Err(io::Error::last_os_error());
        }
        let job = OwnedHandle::from_raw_handle(raw);
        let mut info = JOBOBJECT_EXTENDED_LIMIT_INFORMATION::default();
        info.BasicLimitInformation.LimitFlags = JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE;
        if SetInformationJobObject(
            job.as_raw_handle(),
            JobObjectExtendedLimitInformation,
            (&info as *const JOBOBJECT_EXTENDED_LIMIT_INFORMATION).cast(),
            std::mem::size_of_val(&info) as u32,
        ) == 0
            || AssignProcessToJobObject(job.as_raw_handle(), GetCurrentProcess()) == 0
        {
            return Err(io::Error::last_os_error());
        }
        Ok(job)
    }
}
pub fn run(kernel: &str, home: &str, config: &str, port: &str) -> io::Result<()> {
    let port = port
        .parse::<u16>()
        .ok()
        .filter(|p| *p != 0)
        .ok_or_else(|| io::Error::other("invalid controller port"))?;
    if [kernel, home, config]
        .iter()
        .any(|p| !Path::new(p).is_absolute())
    {
        return Err(io::Error::other("managed kernel paths must be absolute"));
    }
    // The OS closes the job only at process exit, including early errors.
    static JOB: OnceLock<OwnedHandle> = OnceLock::new();
    JOB.set(lifetime_job()?)
        .map_err(|_| io::Error::other("managed mode already started"))?;
    let log = OpenOptions::new()
        .create(true)
        .append(true)
        .open(Path::new(home).join("mihomo.log"))?;
    let mut command = Command::new(kernel);
    if Path::new(kernel)
        .file_name()
        .and_then(|n| n.to_str())
        .is_some_and(|n| {
            n.eq_ignore_ascii_case("clash-rs.exe")
                || n.eq_ignore_ascii_case("clash-rs-x86_64-pc-windows-msvc.exe")
        })
    {
        command.arg("--compatibility");
    }
    let mut child = command
        .args(["-d", home, "-f", config])
        .current_dir(home)
        .creation_flags(CREATE_NO_WINDOW)
        .env_remove("SMARTVPN_MANAGED_RELOAD_TOKEN")
        .env_remove("SMARTVPN_MONITOR_TOKEN")
        .env_remove("SMARTVPN_MONITOR_CONTROLLER_SECRET")
        .stdin(Stdio::null())
        .stdout(log.try_clone()?)
        .stderr(log)
        .spawn()?;
    let pid = child.id();
    let (stop, rx) = mpsc::channel();
    thread::spawn(move || {
        let _ = io::stdin().read(&mut [0u8]);
        let _ = stop.send(());
    });
    thread::spawn(move || loop {
        match child.try_wait() {
            Ok(Some(status)) => process::exit(status.code().unwrap_or(1)),
            Err(_) => process::exit(1),
            Ok(None) => {}
        }
        if !matches!(
            rx.recv_timeout(Duration::from_millis(50)),
            Err(mpsc::RecvTimeoutError::Timeout)
        ) {
            let _ = child.kill();
            let _ = child.wait();
            process::exit(0);
        }
    });
    println!("{pid}");
    smartvpn_host::monitor::serve_managed(
        port,
        std::env::var("SMARTVPN_MANAGED_RELOAD_TOKEN")
            .ok()
            .filter(|s| !s.is_empty()),
    )
}
