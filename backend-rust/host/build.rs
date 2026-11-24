fn main() {
    println!("cargo:rerun-if-changed=jni/android_jni.c");
    if std::env::var("CARGO_CFG_TARGET_OS").as_deref() == Ok("android") {
        cc::Build::new()
            .file("jni/android_jni.c")
            .compile("smartvpn_android_jni");
        println!("cargo:rustc-link-lib=log");
    }
}
