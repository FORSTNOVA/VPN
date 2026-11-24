use crate::control_plane::{
    state::{PlatformBridge, TunnelReport},
    ControlPlane,
};
use serde_json::json;
use std::{
    ffi::{c_char, c_int, CStr, CString},
    path::PathBuf,
    sync::{Arc, Mutex, OnceLock},
};

extern "C" {
    fn smartvpn_jni_bridge_init(class_name: *const c_char) -> c_int;
    fn smartvpn_jni_protect_cb(fd: c_int) -> c_int;
    fn smartvpn_jni_resolve_cb(
        proto: u8,
        src_ip: *const c_char,
        src_port: u16,
        dst_ip: *const c_char,
        dst_port: u16,
        out: *mut c_char,
        cap: usize,
    ) -> i32;
    fn smartvpn_jni_seal(plain: *const c_char) -> *mut c_char;
    fn smartvpn_jni_open(sealed: *const c_char) -> *mut c_char;
    fn smartvpn_jni_describe_tunnel() -> *mut c_char;
    fn smartvpn_jni_drop_tunnel();
    fn smartvpn_jni_free_cstr(ptr: *mut c_char);
    fn smartvpn_jni_on_load(vm: *mut std::ffi::c_void, reserved: *mut std::ffi::c_void) -> c_int;
    fn smartvpn_jni_java_start(
        env: *mut std::ffi::c_void,
        clazz: *mut std::ffi::c_void,
        data_dir: *mut std::ffi::c_void,
        bridge_class: *mut std::ffi::c_void,
    ) -> *mut std::ffi::c_void;
    fn smartvpn_jni_java_stop(env: *mut std::ffi::c_void, clazz: *mut std::ffi::c_void);
    fn smartvpn_jni_java_set_tunnel(
        env: *mut std::ffi::c_void,
        clazz: *mut std::ffi::c_void,
        authorized: u8,
        fd: c_int,
        info: *mut std::ffi::c_void,
    ) -> *mut std::ffi::c_void;
    fn smartvpn_jni_java_configure_rust_core(
        env: *mut std::ffi::c_void,
        clazz: *mut std::ffi::c_void,
        path: *mut std::ffi::c_void,
        enabled: u8,
    ) -> c_int;
}

#[no_mangle]
pub unsafe extern "C" fn JNI_OnLoad(
    vm: *mut std::ffi::c_void,
    reserved: *mut std::ffi::c_void,
) -> c_int {
    smartvpn_jni_on_load(vm, reserved)
}

#[no_mangle]
pub unsafe extern "C" fn Java_com_smartvpn_client_CoreBridge_start(
    env: *mut std::ffi::c_void,
    clazz: *mut std::ffi::c_void,
    data_dir: *mut std::ffi::c_void,
    bridge_class: *mut std::ffi::c_void,
) -> *mut std::ffi::c_void {
    smartvpn_jni_java_start(env, clazz, data_dir, bridge_class)
}

#[no_mangle]
pub unsafe extern "C" fn Java_com_smartvpn_client_CoreBridge_stop(
    env: *mut std::ffi::c_void,
    clazz: *mut std::ffi::c_void,
) {
    smartvpn_jni_java_stop(env, clazz)
}

#[no_mangle]
pub unsafe extern "C" fn Java_com_smartvpn_client_CoreBridge_setTunnel(
    env: *mut std::ffi::c_void,
    clazz: *mut std::ffi::c_void,
    authorized: u8,
    fd: c_int,
    info: *mut std::ffi::c_void,
) -> *mut std::ffi::c_void {
    smartvpn_jni_java_set_tunnel(env, clazz, authorized, fd, info)
}

#[no_mangle]
pub unsafe extern "C" fn Java_com_smartvpn_client_CoreBridge_configureRustCore(
    env: *mut std::ffi::c_void,
    clazz: *mut std::ffi::c_void,
    path: *mut std::ffi::c_void,
    enabled: u8,
) -> c_int {
    smartvpn_jni_java_configure_rust_core(env, clazz, path, enabled)
}

struct AndroidJniBridge;

impl PlatformBridge for AndroidJniBridge {
    fn seal(&self, plain: &str) -> Result<String, String> {
        let c_plain = CString::new(plain).map_err(|_| "NUL in plain".to_owned())?;
        let ptr = unsafe { smartvpn_jni_seal(c_plain.as_ptr()) };
        if ptr.is_null() {
            return Err("keystore seal failed".to_owned());
        }
        let s = unsafe { CStr::from_ptr(ptr) }
            .to_string_lossy()
            .into_owned();
        unsafe { smartvpn_jni_free_cstr(ptr) };
        Ok(s)
    }

    fn open(&self, sealed: &str) -> Result<String, String> {
        let c_sealed = CString::new(sealed).map_err(|_| "NUL in sealed".to_owned())?;
        let ptr = unsafe { smartvpn_jni_open(c_sealed.as_ptr()) };
        if ptr.is_null() {
            return Err("keystore open failed".to_owned());
        }
        let s = unsafe { CStr::from_ptr(ptr) }
            .to_string_lossy()
            .into_owned();
        unsafe { smartvpn_jni_free_cstr(ptr) };
        Ok(s)
    }

    fn drop_tunnel(&self) {
        unsafe { smartvpn_jni_drop_tunnel() };
    }

    fn describe_tunnel(&self) -> Option<TunnelReport> {
        let ptr = unsafe { smartvpn_jni_describe_tunnel() };
        if ptr.is_null() {
            return None;
        }
        let s = unsafe { CStr::from_ptr(ptr) }
            .to_string_lossy()
            .into_owned();
        unsafe { smartvpn_jni_free_cstr(ptr) };
        serde_json::from_str(&s).ok()
    }

    fn protect_hook(&self) -> Option<smartvpn_engine::dialer::SocketProtectHook> {
        Some(rust_protect_trampoline)
    }

    fn resolve_hook(&self) -> Option<smartvpn_engine::ffi::OwnerCallback> {
        Some(rust_resolve_trampoline)
    }
}

extern "C" fn rust_protect_trampoline(fd: c_int) -> c_int {
    unsafe { smartvpn_jni_protect_cb(fd) }
}

extern "C" fn rust_resolve_trampoline(
    proto: u8,
    src_ip: *const c_char,
    src_port: u16,
    dst_ip: *const c_char,
    dst_port: u16,
    out: *mut c_char,
    cap: usize,
) -> i32 {
    unsafe { smartvpn_jni_resolve_cb(proto, src_ip, src_port, dst_ip, dst_port, out, cap) }
}

struct HostInstance {
    rt: tokio::runtime::Runtime,
    cp: Arc<ControlPlane>,
}

static HOST: OnceLock<Mutex<Option<HostInstance>>> = OnceLock::new();

fn host_slot() -> &'static Mutex<Option<HostInstance>> {
    HOST.get_or_init(|| Mutex::new(None))
}

fn c_json(val: serde_json::Value) -> *mut c_char {
    let text = serde_json::to_string(&val).unwrap_or_else(|_| "{}".to_owned());
    CString::new(text)
        .map(CString::into_raw)
        .unwrap_or(std::ptr::null_mut())
}

#[no_mangle]
pub unsafe extern "C" fn smartvpn_host_jni_start(
    data_dir: *const c_char,
    bridge_class: *const c_char,
) -> *mut c_char {
    if data_dir.is_null() || bridge_class.is_null() {
        return c_json(json!({"error": "invalid JNI arguments"}));
    }
    if smartvpn_jni_bridge_init(bridge_class) != 0 {
        return c_json(json!({"error": "could not initialize CoreBridge callbacks"}));
    }
    let dir_str = CStr::from_ptr(data_dir).to_string_lossy();
    let home = PathBuf::from(dir_str.as_ref()).join("SmartVPN");

    let mut guard = host_slot().lock().unwrap();
    if let Some(existing) = guard.as_ref() {
        return c_json(json!({
            "port": existing.cp.port,
            "token": existing.cp.token,
        }));
    }

    let rt = match tokio::runtime::Builder::new_multi_thread()
        .enable_all()
        .build()
    {
        Ok(rt) => rt,
        Err(e) => return c_json(json!({"error": format!("tokio runtime failed: {e}")})),
    };

    match rt.block_on(ControlPlane::start(home, Arc::new(AndroidJniBridge))) {
        Ok(cp) => {
            let reply = json!({
                "port": cp.port,
                "token": cp.token,
            });
            *guard = Some(HostInstance { rt, cp });
            c_json(reply)
        }
        Err(e) => c_json(json!({"error": e})),
    }
}

#[no_mangle]
pub extern "C" fn smartvpn_host_jni_stop() {
    let mut guard = host_slot().lock().unwrap();
    if let Some(inst) = guard.take() {
        inst.rt.block_on(inst.cp.shutdown());
    }
}

#[no_mangle]
pub unsafe extern "C" fn smartvpn_host_jni_set_tunnel(
    authorized: c_int,
    fd: c_int,
    info: *const c_char,
) -> *mut c_char {
    let info_str = if info.is_null() {
        "{}".to_owned()
    } else {
        CStr::from_ptr(info).to_string_lossy().into_owned()
    };
    let guard = host_slot().lock().unwrap();
    let Some(inst) = guard.as_ref() else {
        return c_json(json!({"error": "host not started"}));
    };
    match inst
        .rt
        .block_on(inst.cp.set_tunnel(authorized != 0, fd, &info_str))
    {
        Ok(()) => std::ptr::null_mut(),
        Err(e) => c_json(json!({"error": e})),
    }
}

#[no_mangle]
pub extern "C" fn smartvpn_host_jni_configure_rust_core(
    _path: *const c_char,
    _len: usize,
    _enabled: c_int,
) -> c_int {
    0
}

#[no_mangle]
pub unsafe extern "C" fn smartvpn_host_jni_free_str(ptr: *mut c_char) {
    if !ptr.is_null() {
        drop(CString::from_raw(ptr));
    }
}
