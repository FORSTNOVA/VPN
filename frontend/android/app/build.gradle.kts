plugins {
    id("com.android.application")
    // The Flutter Gradle Plugin must be applied after the Android and Kotlin Gradle plugins.
    id("dev.flutter.flutter-gradle-plugin")
}

// The Go rollback package supplies an explicit false value.
val smartvpnRustCoreEnabled = providers.gradleProperty("smartvpnRustCoreEnabled")
    .map { value ->
        require(value == "true" || value == "false") {
            "smartvpnRustCoreEnabled must be true or false"
        }
        value.toBoolean()
    }.orElse(true)

android {
    providers.gradleProperty("smartvpnNativeDir").orNull?.let {
        sourceSets.getByName("main").jniLibs.setSrcDirs(listOf(it))
    }
    buildFeatures { buildConfig = true }
    namespace = "com.smartvpn.client"
    compileSdk = flutter.compileSdkVersion
    ndkVersion = "27.2.12479018"

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    defaultConfig {
        applicationId = "com.smartvpn.client"
        buildConfigField("boolean", "SMARTVPN_RUST_CORE_ENABLED", smartvpnRustCoreEnabled.get().toString())
        // The kernel is built against API 26 (see the arm64 build of
        // libsmartvpn.so), and 26 is also where the notification channel and
        // foreground service rules this app relies on begin.
        minSdk = 26
        targetSdk = flutter.targetSdkVersion
        versionCode = flutter.versionCode
        versionName = flutter.versionName

        ndk {
            val abis = providers.gradleProperty("smartvpnAndroidAbi").orElse("arm64-v8a").get().split(",")
            require(abis.all { it in setOf("arm64-v8a", "x86_64") }) { "Unsupported SmartVPN ABIs: $abis" }
            abiFilters.clear()
            abiFilters.addAll(abis)
        }
    }

    packaging {
        jniLibs {
            // The kernel is compressed in the package and unpacked when the app
            // is installed. It is loaded as a library rather than executed, so
            // it does not have to be mapped straight out of the APK, and the
            // download is less than half the size because of it.
            useLegacyPackaging = true
        }
    }

    buildTypes {
        release {
            // TODO: Add your own signing config for the release build.
            // Signing with the debug keys for now, so `flutter run --release` works.
            signingConfig = signingConfigs.getByName("debug")
            proguardFiles(getDefaultProguardFile("proguard-android-optimize.txt"), "proguard-rules.pro")
        }
    }
}

kotlin {
    compilerOptions {
        jvmTarget = org.jetbrains.kotlin.gradle.dsl.JvmTarget.JVM_17
    }
}

flutter {
    source = "../.."
}
