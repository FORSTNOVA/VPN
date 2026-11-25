use std::{
    ffi::{c_char, CStr},
    net::TcpStream,
};

/// Negotiate one TCP hop on a connected socket. Call again on the same socket
/// for the next hop. Protocol 1 is HTTP CONNECT and 2 is SOCKS5.
/// Returns 0 on success, -1 for invalid arguments, -2 on transport failure.
///
/// # Safety
/// Supply a live socket owned by the caller with exclusive I/O access and
/// appropriate timeouts. String pointers must be valid NUL-terminated strings.
/// Pass two null credential pointers for anonymous access. Close the socket
/// following a handshake failure.
#[no_mangle]
pub unsafe extern "C" fn smartvpn_engine_connect_v1(
    socket: usize,
    protocol: u32,
    host: *const c_char,
    port: u16,
    username: *const c_char,
    password: *const c_char,
) -> i32 {
    if host.is_null() || port == 0 || username.is_null() != password.is_null() {
        return -1;
    }
    let Ok(host) = CStr::from_ptr(host).to_str() else {
        return -1;
    };
    let credentials = if username.is_null() {
        None
    } else {
        Some((
            CStr::from_ptr(username).to_bytes(),
            CStr::from_ptr(password).to_bytes(),
        ))
    };
    let transport = match protocol {
        1 => crate::chain::Transport::Http(credentials),
        2 => crate::chain::Transport::Socks5(credentials),
        _ => return -1,
    };
    #[cfg(windows)]
    let duplicate = {
        use std::os::windows::io::BorrowedSocket;
        if socket == usize::MAX {
            return -1;
        }
        BorrowedSocket::borrow_raw(socket as _).try_clone_to_owned()
    };
    #[cfg(unix)]
    let duplicate = {
        use std::os::fd::BorrowedFd;
        let Ok(fd) = i32::try_from(socket) else {
            return -1;
        };
        BorrowedFd::borrow_raw(fd).try_clone_to_owned()
    };
    let Ok(handle) = duplicate else { return -2 };
    match crate::chain::connect(TcpStream::from(handle), transport, &[], (host, port)) {
        Ok(_) => 0,
        Err(_) => -2,
    }
}

/// Validate the Clash configuration used by TunStack. Returns 0 on success,
/// -1 for null/invalid UTF-8 and -2 for a configuration error.
/// # Safety
/// yaml must be null or point to a readable NUL-terminated string.
#[no_mangle]
pub unsafe extern "C" fn smartvpn_engine_tun_validate_yaml_v1(yaml: *const c_char) -> i32 {
    if yaml.is_null() {
        return -1;
    }
    let Ok(text) = CStr::from_ptr(yaml).to_str() else {
        return -1;
    };
    match crate::config_bridge::build_router_from_clash_yaml(text, None) {
        Ok(_) => 0,
        Err(err) => {
            eprintln!("TUN configuration: {err}");
            -2
        }
    }
}

pub type OwnerCallback =
    extern "C" fn(u8, *const c_char, u16, *const c_char, u16, *mut c_char, usize) -> i32;

/// Start a Unix TUN stack. The caller retains its fd and can close it after return.
/// Returns a positive handle, or a negative value on startup failure.
/// # Safety
/// yaml must be a readable NUL-terminated string. Callbacks must be thread-safe,
/// remain callable until stop returns, and must not reenter lifecycle operations.
/// protect returns 1 on success. Owner lookup returns a UTF-8 byte count,
/// zero for unknown, or a negative error, and must respect the buffer capacity.
#[no_mangle]
pub unsafe extern "C" fn smartvpn_engine_tun_start_v1(
    tun_fd: i32,
    yaml: *const c_char,
    protect_cb: Option<crate::dialer::SocketProtectHook>,
    resolve_cb: Option<OwnerCallback>,
) -> i64 {
    #[cfg(unix)]
    {
        if yaml.is_null() {
            return -1;
        }
        let Ok(yaml) = CStr::from_ptr(yaml).to_str() else {
            return -1;
        };
        match tun_runtime::start(tun_fd, yaml, protect_cb, resolve_cb) {
            Ok(handle) => handle,
            Err(err) => {
                eprintln!("TUN start: {err}");
                -2
            }
        }
    }
    #[cfg(not(unix))]
    {
        let _ = (tun_fd, yaml, protect_cb, resolve_cb);
        -1
    }
}

/// Return 1 while the worker associated with this handle is alive.
#[no_mangle]
pub extern "C" fn smartvpn_engine_tun_running_v1(handle: i64) -> i32 {
    #[cfg(unix)]
    {
        tun_runtime::running(handle) as i32
    }
    #[cfg(not(unix))]
    {
        let _ = handle;
        0
    }
}

/// Stop and join the stack. Returns -1 for unknown handles or -2 for worker failure.
#[no_mangle]
pub extern "C" fn smartvpn_engine_tun_stop_v1(handle: i64) -> i32 {
    #[cfg(unix)]
    {
        tun_runtime::stop(handle)
    }
    #[cfg(not(unix))]
    {
        let _ = handle;
        -1
    }
}

pub fn tun_router(handle: i64) -> std::io::Result<std::sync::Arc<crate::router::Router>> {
    #[cfg(unix)]
    {
        tun_runtime::router(handle)
    }
    #[cfg(not(unix))]
    {
        Err(std::io::Error::other(format!(
            "no Unix TUN session {handle} on this platform"
        )))
    }
}

#[cfg(unix)]
mod tun_runtime {
    use super::OwnerCallback;
    use crate::{
        config_bridge::build_router_from_clash_yaml,
        dialer::{set_socket_protect_hook, SocketProtectHook},
        router::ProcessResolver,
        tun::{
            android_fd::{open_tun_fd, CallbackProcessResolver},
            TunStack,
        },
    };
    use std::{
        collections::HashMap,
        io,
        sync::{mpsc, Arc, LazyLock, Mutex},
        thread,
    };
    use tokio::sync::oneshot;

    struct Instance {
        router: Arc<crate::router::Router>,
        stop: oneshot::Sender<()>,
        thread: thread::JoinHandle<io::Result<()>>,
    }
    #[derive(Default)]
    struct Registry {
        last_id: i64,
        instances: HashMap<i64, Instance>,
        protect: Option<SocketProtectHook>,
    }
    static REGISTRY: LazyLock<Mutex<Registry>> = LazyLock::new(Default::default);

    pub(super) unsafe fn start(
        fd: i32,
        yaml: &str,
        protect: Option<SocketProtectHook>,
        resolve: Option<OwnerCallback>,
    ) -> io::Result<i64> {
        let resolver = resolve
            .map(|cb| Arc::new(CallbackProcessResolver::new(cb)) as Arc<dyn ProcessResolver>);
        let bundle = build_router_from_clash_yaml(yaml, resolver)?;
        let mut registry = REGISTRY.lock().unwrap();
        if !registry.instances.is_empty()
            && registry.protect.map(|cb| cb as usize) != protect.map(|cb| cb as usize)
        {
            return Err(io::Error::other(
                "active TUN stacks require the same process-wide protector",
            ));
        }
        let id = registry
            .last_id
            .checked_add(1)
            .ok_or_else(|| io::Error::other("TUN handle overflow"))?;
        let (ready, initialized) = mpsc::sync_channel(0);
        let (stop, stopped) = oneshot::channel();
        let router = bundle.router.clone();
        let thread = thread::Builder::new()
            .name(format!("tun-{id}"))
            .spawn(move || {
                let runtime = tokio::runtime::Builder::new_current_thread()
                    .enable_all()
                    .build()?;
                runtime.block_on(async move {
                    let device = open_tun_fd(fd)?;
                    set_socket_protect_hook(protect);
                    ready.send(()).map_err(io::Error::other)?;
                    tokio::select! {
                        result = TunStack::new(bundle.router, None).run(device) => result,
                        result = stopped => result.map_err(io::Error::other),
                    }
                })
            })?;
        if initialized.recv().is_err() {
            return Err(match join(thread) {
                Err(err) => err,
                Ok(()) => io::Error::other("TUN exited before initialization"),
            });
        }
        registry.instances.insert(
            id,
            Instance {
                stop,
                thread,
                router,
            },
        );
        registry.last_id = id;
        registry.protect = protect;
        Ok(id)
    }

    pub(super) fn router(id: i64) -> io::Result<Arc<crate::router::Router>> {
        REGISTRY
            .lock()
            .unwrap()
            .instances
            .get(&id)
            .filter(|i| !i.thread.is_finished())
            .map(|i| i.router.clone())
            .ok_or_else(|| io::Error::other("TUN session is no longer active"))
    }

    fn join(thread: thread::JoinHandle<io::Result<()>>) -> io::Result<()> {
        thread
            .join()
            .map_err(|_| io::Error::other("TUN worker panicked"))?
    }

    pub(super) fn running(id: i64) -> bool {
        REGISTRY
            .lock()
            .unwrap()
            .instances
            .get(&id)
            .is_some_and(|i| !i.thread.is_finished())
    }

    pub(super) fn stop(id: i64) -> i32 {
        let mut registry = REGISTRY.lock().unwrap();
        let Some(instance) = registry.instances.remove(&id) else {
            return -1;
        };
        // A closed receiver means the worker has already exited. Join reports its result.
        let _ = instance.stop.send(());
        let result = join(instance.thread);
        if registry.instances.is_empty() {
            set_socket_protect_hook(None);
            registry.protect = None;
        }
        match result {
            Ok(()) => 0,
            Err(err) => {
                eprintln!("TUN {id}: {err}");
                -2
            }
        }
    }
}
