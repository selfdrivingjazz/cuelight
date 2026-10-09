plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

// Single version source: the repo-root VERSION file (semver). versionCode = major*10000+minor*100+patch.
val cuelightVersion: String = rootProject.file("../../VERSION").let { file ->
    check(file.isFile) { "missing ${file.path}" }
    file.readText().trim()
}
val cuelightVersionCode: Int = Regex("""(\d+)\.(\d+)\.(\d+)(-[0-9A-Za-z.-]+)?""").matchEntire(cuelightVersion)
    ?.destructured?.let { (major, minor, patch) -> major.toInt() * 10000 + minor.toInt() * 100 + patch.toInt() }
    ?: error("VERSION '$cuelightVersion' is not MAJOR.MINOR.PATCH[-PRERELEASE]")

// Release signing: gradle property or environment variable (property wins). Without a keystore the
// release APK is built unsigned (and named so); debug builds always use the SDK debug key.
fun setting(name: String): String? = (findProperty(name) as String?)?.takeIf { it.isNotBlank() } ?: System.getenv(name)?.takeIf { it.isNotBlank() }
val releaseKeystore: String? = setting("CUELIGHT_ANDROID_KEYSTORE")

android {
    namespace = "com.jmilldotdev.cuelight.node"
    compileSdk = 35

    defaultConfig {
        applicationId = "com.jmilldotdev.cuelight.node"
        minSdk = 24
        targetSdk = 35
        versionCode = cuelightVersionCode
        versionName = cuelightVersion
    }

    signingConfigs {
        if (releaseKeystore != null) {
            create("release") {
                val password = setting("CUELIGHT_ANDROID_KEYSTORE_PASSWORD")
                    ?: error("CUELIGHT_ANDROID_KEYSTORE is set but CUELIGHT_ANDROID_KEYSTORE_PASSWORD is not")
                storeFile = file(releaseKeystore)
                storePassword = password
                keyAlias = "cuelight"
                keyPassword = password
            }
        }
    }

    buildTypes {
        // Debug builds use the SDK's auto-generated debug keystore (~/.android/debug.keystore).
        release {
            isMinifyEnabled = false
            signingConfig = signingConfigs.findByName("release")
        }
    }
    buildFeatures { buildConfig = true }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions { jvmTarget = "17" }
}

// Release artifact name from the shared release contract: Cuelight-<v>-android.apk.
android.applicationVariants.all {
    if (buildType.name != "release") return@all
    val suffix = if (signingConfig == null) "-unsigned" else ""
    outputs.all {
        (this as com.android.build.gradle.internal.api.BaseVariantOutputImpl).outputFileName = "Cuelight-$cuelightVersion-android$suffix.apk"
    }
}

dependencies {
    testImplementation("junit:junit:4.13.2")
    testImplementation("org.json:json:20240303")
}
