plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

android {
    namespace = "com.huemux.app"
    compileSdk = 35

    defaultConfig {
        applicationId = "com.huemux.app"
        // 26 matches the -androidapi passed to gomobile bind. Below that,
        // gomobile's own runtime support gets shaky and there is no reason to
        // court it for devices this would never be installed on.
        minSdk = 26
        targetSdk = 35
        versionCode = 1
        versionName = "0.0.1-alpha"
    }

    // Release signing comes from the environment when CI has a keystore
    // secret configured. Absent that, no signingConfig is registered at all
    // and only assembleDebug is usable — better than a half-configured
    // release build that fails deep inside Gradle with an unhelpful message.
    val keystorePath = System.getenv("HUEMUX_KEYSTORE")
    if (!keystorePath.isNullOrBlank()) {
        signingConfigs {
            create("release") {
                storeFile = file(keystorePath)
                storePassword = System.getenv("HUEMUX_KEYSTORE_PASSWORD")
                keyAlias = System.getenv("HUEMUX_KEY_ALIAS")
                keyPassword = System.getenv("HUEMUX_KEY_PASSWORD")
            }
        }
    }

    buildTypes {
        release {
            // No shrinking: the payload is the Go runtime inside the .aar,
            // which R8 cannot touch anyway, so it would cost build time and
            // add a way to break the JNI bindings for no size win.
            isMinifyEnabled = false
            if (!keystorePath.isNullOrBlank()) {
                signingConfig = signingConfigs.getByName("release")
            }
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions {
        jvmTarget = "17"
    }

    // arm64 only, matching the AAR the CI workflow builds. Every current
    // Android device is arm64; shipping four ABIs would multiply the ~6MB Go
    // runtime for architectures nobody will install this on.
    packaging {
        jniLibs {
            useLegacyPackaging = false
        }
    }
}

dependencies {
    // Produced by `gomobile bind` in .github/workflows/android.yml and dropped
    // into app/libs/. Not committed — it is a build artifact containing the
    // whole Go core, and a 6MB binary does not belong in git.
    implementation(files("libs/huemux.aar"))

    implementation("androidx.appcompat:appcompat:1.7.0")
    implementation("androidx.activity:activity-ktx:1.9.3")
}
