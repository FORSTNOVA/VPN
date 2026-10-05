/*
 * Every JNI call this app makes lives in this file, so that the Go side deals
 * in strings and integers and never in a JNIEnv. Two directions meet here: the
 * methods Java calls on CoreBridge, whose C names carry the class name because
 * that is how the JVM finds a native method, and the few helpers Go calls back
 * — sealing a secret with the Android keystore, and protecting a socket so the
 * kernel's own connections do not re-enter the tunnel they carry.
 *
 * Strings cross as modified UTF-8, which is what GetStringUTFChars gives. Every
 * value that crosses here is a path, a URL or base64 — all of it inside the
 * basic multilingual plane — so the difference from UTF-8 does not arise.
 */

#include <jni.h>
#include <stdlib.h>
#include <string.h>

#include "_cgo_export.h"

static JavaVM *g_vm = NULL;
static jclass g_bridge = NULL;
static jmethodID g_seal = NULL;
static jmethodID g_open = NULL;
static jmethodID g_protect = NULL;
static jmethodID g_drop_tunnel = NULL;
static jmethodID g_describe_tunnel = NULL;

JNIEXPORT jint JNICALL JNI_OnLoad(JavaVM *vm, void *reserved) {
    (void)reserved;
    g_vm = vm;
    return JNI_VERSION_1_6;
}

/*
 * attach returns an environment for the calling thread, attaching it to the JVM
 * if this call is coming from a Go thread rather than a Java one. The thread is
 * deliberately left attached: Go reuses its threads, so the alternative is
 * attaching and detaching on every outbound connection.
 */
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

static void clear_exception(JNIEnv *env) {
    if ((*env)->ExceptionCheck(env)) {
        (*env)->ExceptionDescribe(env);
        (*env)->ExceptionClear(env);
    }
}

/*
 * smartvpn_bridge_init caches the class the callbacks live on and the three
 * methods Go calls. It runs on a Java thread, which is the only place FindClass
 * is reliable: on a thread the runtime created itself, the application class
 * loader is not the one in effect.
 */
int smartvpn_bridge_init(const char *className) {
    int attached = 0;
    JNIEnv *env = attach(&attached);
    if (env == NULL) {
        return -1;
    }
    jclass local = (*env)->FindClass(env, className);
    if (local == NULL) {
        clear_exception(env);
        return -1;
    }
    g_bridge = (jclass)(*env)->NewGlobalRef(env, local);
    (*env)->DeleteLocalRef(env, local);
    if (g_bridge == NULL) {
        return -1;
    }
    g_seal = (*env)->GetStaticMethodID(env, g_bridge, "seal",
                                       "(Ljava/lang/String;)Ljava/lang/String;");
    g_open = (*env)->GetStaticMethodID(env, g_bridge, "open",
                                       "(Ljava/lang/String;)Ljava/lang/String;");
    g_protect = (*env)->GetStaticMethodID(env, g_bridge, "protect", "(I)Z");
    g_drop_tunnel = (*env)->GetStaticMethodID(env, g_bridge, "dropTunnel", "()V");
    g_describe_tunnel = (*env)->GetStaticMethodID(env, g_bridge, "describeTunnelNow",
                                                  "()Ljava/lang/String;");
    if (g_seal == NULL || g_open == NULL || g_protect == NULL || g_drop_tunnel == NULL ||
        g_describe_tunnel == NULL) {
        clear_exception(env);
        return -1;
    }
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

int smartvpn_protect(int fd) {
    if (g_bridge == NULL || g_protect == NULL) {
        return -1;
    }
    int attached = 0;
    JNIEnv *env = attach(&attached);
    if (env == NULL) {
        return -1;
    }
    jboolean protected = (*env)->CallStaticBooleanMethod(env, g_bridge, g_protect, (jint)fd);
    if ((*env)->ExceptionCheck(env)) {
        clear_exception(env);
        return -1;
    }
    return protected == JNI_TRUE ? 0 : -1;
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
        return NULL;
    }
    jstring output = (jstring)(*env)->CallStaticObjectMethod(env, g_bridge, method, input);
    (*env)->DeleteLocalRef(env, input);
    if ((*env)->ExceptionCheck(env)) {
        clear_exception(env);
        return NULL;
    }
    char *copy = copy_string(env, output);
    if (output != NULL) {
        (*env)->DeleteLocalRef(env, output);
    }
    return copy;
}

char *smartvpn_seal(const char *plain) {
    return call_string_method(g_seal, plain);
}

char *smartvpn_open(const char *sealed) {
    return call_string_method(g_open, sealed);
}

/*
 * smartvpn_describe_tunnel asks Java for the tunnel's description as it stands
 * now. It is a query rather than something remembered, because the system fills
 * a tunnel in over more than one step.
 */
char *smartvpn_describe_tunnel(void) {
    return call_string_method(g_describe_tunnel, "");
}

/*
 * smartvpn_drop_tunnel is called from a Go thread when the kernel has gone: the
 * tunnel has to follow it, or every application's traffic keeps being routed
 * into an interface nobody reads.
 */
void smartvpn_drop_tunnel(void) {
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
}

/*
 * The three entry points Java calls. Each hands its arguments to Go and returns
 * what Go answered, which is a JSON string the Java side parses.
 */

JNIEXPORT jstring JNICALL Java_com_smartvpn_client_CoreBridge_start(
    JNIEnv *env, jclass clazz, jstring dataDir, jstring bridgeClass) {
    (void)clazz;
    const char *dir = (*env)->GetStringUTFChars(env, dataDir, NULL);
    const char *name = (*env)->GetStringUTFChars(env, bridgeClass, NULL);
    if (dir == NULL || name == NULL) {
        clear_exception(env);
        return NULL;
    }
    char *answer = SmartVPNStart((char *)dir, (char *)name);
    (*env)->ReleaseStringUTFChars(env, dataDir, dir);
    (*env)->ReleaseStringUTFChars(env, bridgeClass, name);
    if (answer == NULL) {
        return NULL;
    }
    jstring result = (*env)->NewStringUTF(env, answer);
    free(answer);
    return result;
}

JNIEXPORT void JNICALL Java_com_smartvpn_client_CoreBridge_stop(JNIEnv *env, jclass clazz) {
    (void)env;
    (void)clazz;
    SmartVPNStop();
}

JNIEXPORT jstring JNICALL Java_com_smartvpn_client_CoreBridge_setTunnel(
    JNIEnv *env, jclass clazz, jboolean authorized, jint fd, jstring info) {
    (void)clazz;
    const char *text = (*env)->GetStringUTFChars(env, info, NULL);
    if (text == NULL) {
        clear_exception(env);
        return NULL;
    }
    char *answer = SmartVPNSetTunnel(authorized == JNI_TRUE ? 1 : 0, (int)fd, (char *)text);
    (*env)->ReleaseStringUTFChars(env, info, text);
    if (answer == NULL) {
        return NULL;
    }
    jstring result = (*env)->NewStringUTF(env, answer);
    free(answer);
    return result;
}
