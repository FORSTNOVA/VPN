#include <jni.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

/* Implemented in Rust (backend-rust/host/src/android_jni.rs). */
extern char *smartvpn_host_jni_start(const char *data_dir, const char *bridge_class);
extern void smartvpn_host_jni_stop(void);
extern char *smartvpn_host_jni_set_tunnel(int authorized, int fd, const char *info);
extern int smartvpn_host_jni_configure_rust_core(const char *path, size_t len, int enabled);
extern void smartvpn_host_jni_free_str(char *ptr);

static JavaVM *g_vm = NULL;
static jclass g_bridge = NULL;
static jmethodID g_seal = NULL;
static jmethodID g_open = NULL;
static jmethodID g_protect = NULL;
static jmethodID g_drop_tunnel = NULL;
static jmethodID g_describe_tunnel = NULL;
static jmethodID g_resolve_owner_package = NULL;

jint smartvpn_jni_on_load(JavaVM *vm, void *reserved) {
    (void)reserved;
    g_vm = vm;
    return JNI_VERSION_1_6;
}

static JNIEnv *attach(int *attached) {
    JNIEnv *env = NULL;
    *attached = 0;
    if (g_vm == NULL) {
        return NULL;
    }
    jint status = (*g_vm)->GetEnv(g_vm, (void **)&env, JNI_VERSION_1_6);
    if (status == JNI_EDETACHED) {
        if ((*g_vm)->AttachCurrentThread(g_vm, &env, NULL) != JNI_OK) {
            return NULL;
        }
        *attached = 1;
    } else if (status != JNI_OK) {
        return NULL;
    }
    return env;
}

static void detach_if_needed(int attached) {
    if (attached && g_vm != NULL) {
        (*g_vm)->DetachCurrentThread(g_vm);
    }
}

static void clear_exception(JNIEnv *env) {
    if ((*env)->ExceptionCheck(env)) {
        (*env)->ExceptionDescribe(env);
        (*env)->ExceptionClear(env);
    }
}

int smartvpn_jni_bridge_init(const char *class_name) {
    int attached = 0;
    JNIEnv *env = attach(&attached);
    if (env == NULL) {
        return -1;
    }
    jclass local = (*env)->FindClass(env, class_name);
    if (local == NULL) {
        clear_exception(env);
        detach_if_needed(attached);
        return -1;
    }
    if (g_bridge != NULL) {
        int same = (*env)->IsSameObject(env, g_bridge, local);
        (*env)->DeleteLocalRef(env, local);
        detach_if_needed(attached);
        return same ? 0 : -1;
    }
    if (!(g_seal = (*env)->GetStaticMethodID(env, local, "seal", "(Ljava/lang/String;)Ljava/lang/String;")) ||
        !(g_open = (*env)->GetStaticMethodID(env, local, "open", "(Ljava/lang/String;)Ljava/lang/String;")) ||
        !(g_protect = (*env)->GetStaticMethodID(env, local, "protect", "(I)Z")) ||
        !(g_drop_tunnel = (*env)->GetStaticMethodID(env, local, "dropTunnel", "()V")) ||
        !(g_describe_tunnel = (*env)->GetStaticMethodID(env, local, "describeTunnelNow", "()Ljava/lang/String;")) ||
        !(g_resolve_owner_package = (*env)->GetStaticMethodID(env, local, "resolveOwnerPackage",
            "(ILjava/lang/String;ILjava/lang/String;I)Ljava/lang/String;"))) {
        clear_exception(env);
        (*env)->DeleteLocalRef(env, local);
        detach_if_needed(attached);
        return -1;
    }
    g_bridge = (jclass)(*env)->NewGlobalRef(env, local);
    (*env)->DeleteLocalRef(env, local);
    if (g_bridge == NULL) {
        clear_exception(env);
        detach_if_needed(attached);
        return -1;
    }
    detach_if_needed(attached);
    return 0;
}

static char *copy_string(JNIEnv *env, jstring value) {
    if (value == NULL) {
        return NULL;
    }
    const char *chars = (*env)->GetStringUTFChars(env, value, NULL);
    if (chars == NULL) {
        clear_exception(env);
        return NULL;
    }
    char *copy = strdup(chars);
    (*env)->ReleaseStringUTFChars(env, value, chars);
    return copy;
}

int smartvpn_jni_protect(int fd) {
    if (g_bridge == NULL || g_protect == NULL) {
        return -1;
    }
    int attached = 0;
    JNIEnv *env = attach(&attached);
    if (env == NULL) {
        return -1;
    }
    jboolean ok = (*env)->CallStaticBooleanMethod(env, g_bridge, g_protect, (jint)fd);
    if ((*env)->ExceptionCheck(env)) {
        clear_exception(env);
        detach_if_needed(attached);
        return -1;
    }
    detach_if_needed(attached);
    return ok == JNI_TRUE ? 0 : -1;
}

int smartvpn_jni_protect_cb(int fd) {
    return smartvpn_jni_protect(fd) == 0 ? 1 : 0;
}

char *smartvpn_jni_resolve_owner_package(int protocol, const char *src_ip, int src_port,
                                         const char *dst_ip, int dst_port) {
    int attached = 0;
    JNIEnv *env = attach(&attached);
    if (env == NULL || g_bridge == NULL || g_resolve_owner_package == NULL) {
        detach_if_needed(attached);
        return NULL;
    }
    if ((*env)->PushLocalFrame(env, 4) < 0) {
        clear_exception(env);
        detach_if_needed(attached);
        return NULL;
    }
    char *package = NULL;
    jstring src = (*env)->NewStringUTF(env, src_ip ? src_ip : "");
    if (src == NULL) goto done;
    jstring dst = (*env)->NewStringUTF(env, dst_ip ? dst_ip : "");
    if (dst == NULL) goto done;
    jstring result = (jstring)(*env)->CallStaticObjectMethod(env, g_bridge, g_resolve_owner_package,
        (jint)protocol, src, (jint)src_port, dst, (jint)dst_port);
    if (!(*env)->ExceptionCheck(env)) {
        package = copy_string(env, result);
    }
done:
    clear_exception(env);
    (*env)->PopLocalFrame(env, NULL);
    detach_if_needed(attached);
    return package;
}

int32_t smartvpn_jni_resolve_cb(uint8_t proto, const char *src_ip, uint16_t src_port,
                                const char *dst_ip, uint16_t dst_port, char *out, size_t cap) {
    char *owner = smartvpn_jni_resolve_owner_package((int)proto, src_ip, (int)src_port, dst_ip, (int)dst_port);
    if (!owner) {
        return 0;
    }
    size_t len = strlen(owner);
    int32_t count = -1;
    if (len <= cap && len <= INT32_MAX) {
        memcpy(out, owner, len);
        count = (int32_t)len;
    }
    free(owner);
    return count;
}

static char *call_string_method(jmethodID method, const char *value) {
    if (g_bridge == NULL || method == NULL || value == NULL) {
        return NULL;
    }
    int attached = 0;
    JNIEnv *env = attach(&attached);
    if (env == NULL) {
        return NULL;
    }
    jstring input = (*env)->NewStringUTF(env, value);
    if (input == NULL) {
        clear_exception(env);
        detach_if_needed(attached);
        return NULL;
    }
    jstring output = (jstring)(*env)->CallStaticObjectMethod(env, g_bridge, method, input);
    (*env)->DeleteLocalRef(env, input);
    if ((*env)->ExceptionCheck(env)) {
        clear_exception(env);
        detach_if_needed(attached);
        return NULL;
    }
    char *copy = copy_string(env, output);
    if (output != NULL) {
        (*env)->DeleteLocalRef(env, output);
    }
    detach_if_needed(attached);
    return copy;
}

char *smartvpn_jni_seal(const char *plain) {
    return call_string_method(g_seal, plain);
}

char *smartvpn_jni_open(const char *sealed) {
    return call_string_method(g_open, sealed);
}

char *smartvpn_jni_describe_tunnel(void) {
    return call_string_method(g_describe_tunnel, "");
}

void smartvpn_jni_drop_tunnel(void) {
    if (g_bridge == NULL || g_drop_tunnel == NULL) {
        return;
    }
    int attached = 0;
    JNIEnv *env = attach(&attached);
    if (env == NULL) {
        return;
    }
    (*env)->CallStaticVoidMethod(env, g_bridge, g_drop_tunnel);
    if ((*env)->ExceptionCheck(env)) {
        clear_exception(env);
    }
    detach_if_needed(attached);
}

void smartvpn_jni_free_cstr(char *ptr) {
    free(ptr);
}

jstring smartvpn_jni_java_start(
    JNIEnv *env, jclass clazz, jstring dataDir, jstring bridgeClass) {
    (void)clazz;
    const char *dir = (*env)->GetStringUTFChars(env, dataDir, NULL);
    const char *name = (*env)->GetStringUTFChars(env, bridgeClass, NULL);
    if (dir == NULL || name == NULL) {
        if (dir) (*env)->ReleaseStringUTFChars(env, dataDir, dir);
        if (name) (*env)->ReleaseStringUTFChars(env, bridgeClass, name);
        clear_exception(env);
        return NULL;
    }
    char *answer = smartvpn_host_jni_start(dir, name);
    (*env)->ReleaseStringUTFChars(env, dataDir, dir);
    (*env)->ReleaseStringUTFChars(env, bridgeClass, name);
    if (answer == NULL) {
        return NULL;
    }
    jstring result = (*env)->NewStringUTF(env, answer);
    smartvpn_host_jni_free_str(answer);
    return result;
}

void smartvpn_jni_java_stop(JNIEnv *env, jclass clazz) {
    (void)env;
    (void)clazz;
    smartvpn_host_jni_stop();
}

jstring smartvpn_jni_java_set_tunnel(
    JNIEnv *env, jclass clazz, jboolean authorized, jint fd, jstring info) {
    (void)clazz;
    const char *text = (*env)->GetStringUTFChars(env, info, NULL);
    if (text == NULL) {
        clear_exception(env);
        return NULL;
    }
    char *answer = smartvpn_host_jni_set_tunnel(authorized == JNI_TRUE ? 1 : 0, (int)fd, text);
    (*env)->ReleaseStringUTFChars(env, info, text);
    if (answer == NULL) {
        return NULL;
    }
    jstring result = (*env)->NewStringUTF(env, answer);
    smartvpn_host_jni_free_str(answer);
    return result;
}

jint smartvpn_jni_java_configure_rust_core(
    JNIEnv *env, jclass clazz, jbyteArray path, jboolean enabled) {
    (void)clazz;
    if (path == NULL) return -1;
    jsize length = (*env)->GetArrayLength(env, path);
    if (length < 0 || length > 4096) return -1;
    char bytes[4096];
    if (length > 0) {
        (*env)->GetByteArrayRegion(env, path, 0, length, (jbyte *)bytes);
        if ((*env)->ExceptionCheck(env)) return -1;
    }
    return (jint)smartvpn_host_jni_configure_rust_core(bytes, (size_t)length,
                                                      enabled == JNI_TRUE ? 1 : 0);
}
