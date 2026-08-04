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
        // Both come from CI, which derives them from the tag being built.
        //
        // These were hardcoded, and versionCode in particular is not cosmetic:
        // Android refuses to install a build whose versionCode is not greater
        // than the installed one, so every release shipping versionCode 1 was
        // an app that could never update itself. That is invisible while
        // testers sideload each APK over a wiped install, and fatal the moment
        // anything tracks releases — which is the entire point of publishing
        // through Obtainium or F-Droid.
        //
        // The code is the commit count: monotonic, deterministic from the
        // source alone (so a rebuild of the same tag produces the same number,
        // which F-Droid's reproducibility checks care about), and never needs
        // a human to remember to bump it.
        versionCode = (System.getenv("HUEMUX_VERSION_CODE") ?: "1").toInt()
        versionName = System.getenv("HUEMUX_VERSION_NAME") ?: "dev"
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

    // AGP 8 stopped generating BuildConfig unless asked. MainActivity reads
    // BuildConfig.VERSION_NAME to tell the Go side what version it is, so
    // without this the app simply does not compile.
    buildFeatures {
        buildConfig = true
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

    testOptions {
        unitTests {
            // HueLog routes through android.util.Log, which the unit-test stub
            // otherwise throws on. Returning defaults lets the tests exercise
            // the routing logic without a device or Robolectric.
            isReturnDefaultValues = true
        }
    }
}

dependencies {
    // Produced by `gomobile bind` in .github/workflows/android.yml and dropped
    // into app/libs/. Not committed — it is a build artifact containing the
    // whole Go core, and a 6MB binary does not belong in git.
    implementation(files("libs/huemux.aar"))

    implementation("androidx.appcompat:appcompat:1.7.0")
    // Used directly for window-inset handling; transitive availability is not
    // something to rely on for an API the code calls by name.
    implementation("androidx.core:core-ktx:1.15.0")
    implementation("androidx.activity:activity-ktx:1.9.3")

    // Kotlin unit tests (android/app/src/test). Run in CI via
    // testDebugUnitTest; isReturnDefaultValues above keeps the Android stubs
    // quiet so pure-logic tests need no device and no Robolectric.
    testImplementation("junit:junit:4.13.2")
}
