// swift-tools-version:5.9
import PackageDescription

let package = Package(
    name: "silo",
    platforms: [
        .macOS(.v13)
    ],
    products: [
        .executable(name: "silo", targets: ["Silo"])
    ],
    dependencies: [
        .package(url: "https://github.com/apple/swift-argument-parser.git", from: "1.3.0"),
    ],
    targets: [
        .executableTarget(
            name: "Silo",
            dependencies: [
                .product(name: "ArgumentParser", package: "swift-argument-parser"),
                "CLI",
                "Config",
                "Docker",
            ],
            resources: [
                .copy("Resources/Dockerfile")
            ]
        ),
        .target(name: "CLI"),
        .target(name: "Config"),
        .target(name: "Docker"),
        .testTarget(
            name: "SiloTests",
            dependencies: ["Silo", "CLI", "Config", "Docker"]
        ),
    ]
)
