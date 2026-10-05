# The Go library calls these methods by JNI name. R8 cannot see those calls,
# so keep the bridge class and its members present and unmangled in Release APKs.
-keep class com.smartvpn.client.CoreBridge { *; }
