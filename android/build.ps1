param(
    [Parameter(ValueFromRemainingArguments = $true)]
    [string[]]$GradleArgs
)

$repositoryRoot = Split-Path -Parent $PSScriptRoot
$toolchainRoot = Join-Path $repositoryRoot '.runtime\android-toolchain'
$jdkRoot = Join-Path $toolchainRoot 'jdk\jdk-17.0.20.1+1'
$gradleExecutable = Join-Path $toolchainRoot 'gradle\gradle-9.6.0\bin\gradle.bat'
$sdkRoot = Join-Path $toolchainRoot 'sdk'

if (-not (Test-Path -LiteralPath (Join-Path $jdkRoot 'bin\java.exe'))) {
    throw "JDK 17 is missing: $jdkRoot"
}
if (-not (Test-Path -LiteralPath $gradleExecutable)) {
    throw "Gradle 9.6 is missing: $gradleExecutable"
}
if (-not (Test-Path -LiteralPath (Join-Path $sdkRoot 'platforms\android-36\android.jar'))) {
    throw "Android SDK 36 is missing: $sdkRoot"
}

$env:JAVA_HOME = $jdkRoot
$env:ANDROID_HOME = $sdkRoot
$env:ANDROID_SDK_ROOT = $sdkRoot
$env:GRADLE_USER_HOME = Join-Path $toolchainRoot 'gradle-home'
$env:PATH = (Join-Path $jdkRoot 'bin') + ';' + $env:PATH

if (-not $GradleArgs -or $GradleArgs.Count -eq 0) {
    $GradleArgs = @(':app:assembleDebug')
}

& $gradleExecutable -p $PSScriptRoot @GradleArgs
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
