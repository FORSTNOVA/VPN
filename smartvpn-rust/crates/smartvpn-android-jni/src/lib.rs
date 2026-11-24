#![cfg(target_os = "android")]
use anyhow::{bail, ensure, Context, Result};
use jni::{
    objects::{GlobalRef, JByteArray, JClass, JString, JValue},
    sys::{jboolean, jint, jstring, JNI_TRUE},
    JNIEnv, JavaVM,
};
use serde_json::json;
use smartvpn_runtime::Runtime;
use std::{
    os::fd::{AsRawFd, FromRawFd, OwnedFd},
    ptr,
    sync::{Arc, Mutex},
};

struct Bridge {
    vm: JavaVM,
    class: GlobalRef,
}
impl Bridge {
    fn owner_package(
        &self,
        protocol: u8,
        src: std::net::SocketAddr,
        dst: std::net::SocketAddr,
    ) -> Result<Option<String>> {
        let mut env = self.vm.attach_current_thread()?;
        env.with_local_frame(4, |env| {
            let src_ip = env.new_string(src.ip().to_string())?;
            let dst_ip = env.new_string(dst.ip().to_string())?;
            let result = env
                .call_static_method(
                    <&JClass>::from(self.class.as_obj()),
                    "resolveOwnerPackage",
                    "(ILjava/lang/String;ILjava/lang/String;I)Ljava/lang/String;",
                    &[
                        JValue::Int(protocol.into()),
                        JValue::Object(&src_ip),
                        JValue::Int(src.port().into()),
                        JValue::Object(&dst_ip),
                        JValue::Int(dst.port().into()),
                    ],
                )?
                .l()?;
            let package: String = env.get_string(&JString::from(result))?.into();
            Ok((!package.is_empty()).then_some(package))
        })
    }
    fn protect(&self, fd: i32) -> Result<()> {
        let mut env = self.vm.attach_current_thread()?;
        ensure!(
            env.call_static_method(
                <&JClass>::from(self.class.as_obj()),
                "protect",
                "(I)Z",
                &[JValue::Int(fd)]
            )?
            .z()?,
            "VpnService.protect refused socket"
        );
        Ok(())
    }
    fn drop_tunnel(&self) -> Result<()> {
        let mut env = self.vm.attach_current_thread()?;
        env.call_static_method(
            <&JClass>::from(self.class.as_obj()),
            "dropTunnel",
            "()V",
            &[],
        )?;
        Ok(())
    }
}
mod embedded;
use embedded::Android;
struct Service {
    runtime: Runtime,
    plane: Arc<Mutex<Android>>,
}
static SERVICE: Mutex<Option<Service>> = Mutex::new(None);

fn string_result(env: &mut JNIEnv, result: Result<String>) -> jstring {
    let value = match result {
        Ok(value) => value,
        Err(e) => json!({"error":format!("{e:#}")}).to_string(),
    };
    match env.new_string(value) {
        Ok(s) => s.into_raw(),
        Err(e) => {
            eprintln!("JNI string allocation failed: {e}");
            ptr::null_mut()
        }
    }
}
#[no_mangle]
pub extern "system" fn Java_com_smartvpn_client_CoreBridge_readGameBypass(
    mut env: JNIEnv,
    _: JClass,
    dir: JString,
) -> jstring {
    let result = (|| -> Result<String> {
        let dir: String = env.get_string(&dir)?.into();
        let games = smartvpn_runtime::android_game_bypass(std::path::Path::new(&dir))?;
        Ok(json!({"games": games}).to_string())
    })();
    string_result(&mut env, result)
}

#[no_mangle]
pub extern "system" fn Java_com_smartvpn_client_CoreBridge_configureRustCore(
    env: JNIEnv,
    _: JClass,
    path: JByteArray,
    enabled: jboolean,
) -> jint {
    if SERVICE.lock().unwrap().is_some() {
        return -2;
    }
    if enabled != JNI_TRUE {
        return -1;
    }
    match env.convert_byte_array(path) {
        Ok(path)
            if std::str::from_utf8(&path)
                .is_ok_and(|p| p.starts_with('/') && !p.contains('\0')) =>
        {
            0
        }
        _ => -1,
    }
}
#[no_mangle]
pub extern "system" fn Java_com_smartvpn_client_CoreBridge_start(
    mut env: JNIEnv,
    class: JClass,
    dir: JString,
    bridge: JString,
) -> jstring {
    let result = (|| -> Result<String> {
        let mut service = SERVICE.lock().unwrap();
        if service.as_ref().is_some_and(|s| s.runtime.is_finished()) {
            service.take().unwrap().runtime.wait()?;
        }
        if let Some(s) = service.as_ref() {
            return Ok(serde_json::to_string(&s.runtime.bootstrap)?);
        }
        let data_dir: String = env.get_string(&dir)?.into();
        ensure!(
            std::path::Path::new(&data_dir).is_dir(),
            "invalid Android data directory"
        );
        let name: String = env.get_string(&bridge)?.into();
        ensure!(
            name == "com/smartvpn/client/CoreBridge",
            "unexpected bridge class"
        );
        let plane = Arc::new(Mutex::new(Android::new(
            Bridge {
                vm: env.get_java_vm()?,
                class: env.new_global_ref(class)?,
            },
            data_dir.clone(),
        )));
        let runtime = Runtime::start(plane.clone(), std::path::Path::new(&data_dir))?;
        let answer = serde_json::to_string(&runtime.bootstrap)?;
        *service = Some(Service { runtime, plane });
        Ok(answer)
    })();
    string_result(&mut env, result)
}
#[no_mangle]
pub extern "system" fn Java_com_smartvpn_client_CoreBridge_stop(mut env: JNIEnv, _: JClass) {
    let mut service = SERVICE.lock().unwrap();
    if let Some(s) = service.take() {
        if let Err(e) = s.runtime.stop() {
            env.throw_new("java/lang/IllegalStateException", format!("{e:#}"))
                .expect("JNI exception delivery");
        }
    }
}
#[no_mangle]
pub extern "system" fn Java_com_smartvpn_client_CoreBridge_setTunnel(
    mut env: JNIEnv,
    _: JClass,
    authorized: jboolean,
    fd: jint,
    info: JString,
) -> jstring {
    let result = (|| -> Result<String> {
        let service = SERVICE.lock().unwrap();
        if authorized != JNI_TRUE {
            if let Some(s) = service.as_ref() {
                let mut plane = s.plane.lock().unwrap();
                plane.release()?;
                plane.retired_fd = None;
            }
            return Ok(String::new());
        }
        ensure!(fd >= 0, "invalid detached fd");
        let Some(s) = service.as_ref() else {
            drop(unsafe { OwnedFd::from_raw_fd(fd) });
            bail!("service has not started");
        };
        let mut plane = s.plane.lock().unwrap();
        ensure!(
            plane.retired_fd != Some(fd),
            "VpnService is releasing this tunnel"
        );
        if plane.fd.as_ref().map(AsRawFd::as_raw_fd) != Some(fd) {
            let incoming = unsafe { OwnedFd::from_raw_fd(fd) };
            plane.release()?;
            plane.fd = Some(incoming);
        }
        let raw: String = env.get_string(&info)?.into();
        plane.info = Some(serde_json::from_str(&raw).context("invalid tunnel report")?);
        Ok(String::new())
    })();
    string_result(&mut env, result)
}

#[no_mangle]
pub extern "system" fn Java_com_smartvpn_client_CoreBridge_notifyNetworkChanged(
    mut env: JNIEnv,
    _: JClass,
) {
    let service = SERVICE.lock().unwrap();
    if let Some(service) = service.as_ref() {
        if let Err(error) = service.plane.lock().unwrap().network_changed() {
            env.throw_new("java/lang/IllegalStateException", error.to_string())
                .expect("JNI exception delivery");
        }
    }
}
