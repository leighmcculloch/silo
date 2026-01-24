import Foundation

/// ANSI color codes for terminal styling
public enum ANSIColor: String {
    case reset = "\u{001B}[0m"
    case bold = "\u{001B}[1m"

    // Colors matching the lipgloss colors from Go version
    case pink = "\u{001B}[38;5;205m"      // Color 205 (title, bullet)
    case gray = "\u{001B}[38;5;241m"      // Color 241 (subtitle, dim)
    case cyan = "\u{001B}[38;5;86m"       // Color 86 (info)
    case green = "\u{001B}[38;5;82m"      // Color 82 (success)
    case yellow = "\u{001B}[38;5;214m"    // Color 214 (warning)
    case red = "\u{001B}[38;5;196m"       // Color 196 (error)

    // For config syntax highlighting
    case configKey = "\u{001B}[38;5;6m"   // Cyan
    case configString = "\u{001B}[38;5;2m" // Green
    case configComment = "\u{001B}[38;5;8m" // Gray
}

/// Check if a file handle is a TTY
public func isTerminal(_ fileHandle: FileHandle) -> Bool {
    return isatty(fileHandle.fileDescriptor) != 0
}

/// Style text with an ANSI color
public func styled(_ text: String, _ color: ANSIColor) -> String {
    return "\(color.rawValue)\(text)\(ANSIColor.reset.rawValue)"
}

/// Style text as bold
public func bold(_ text: String) -> String {
    return "\(ANSIColor.bold.rawValue)\(text)\(ANSIColor.reset.rawValue)"
}

/// CLI provides styled output functions for the CLI
public enum CLI {
    /// Log prints an informational message with a prefix to stderr
    public static func log(_ format: String, _ args: CVarArg...) {
        logTo(FileHandle.standardError, format, args)
    }

    /// LogTo prints an informational message with a prefix to the given file handle
    public static func logTo(_ handle: FileHandle, _ format: String, _ args: [CVarArg]) {
        let msg = String(format: format, arguments: args)
        let output = styled("==> \(msg)", .cyan)
        fputs(output + "\n", handle == FileHandle.standardError ? stderr : stdout)
    }

    /// LogSuccess prints a success message to stderr
    public static func logSuccess(_ format: String, _ args: CVarArg...) {
        logSuccessTo(FileHandle.standardError, format, args)
    }

    /// LogSuccessTo prints a success message to the given file handle
    public static func logSuccessTo(_ handle: FileHandle, _ format: String, _ args: [CVarArg]) {
        let msg = String(format: format, arguments: args)
        let output = styled("✓ \(msg)", .green)
        fputs(output + "\n", handle == FileHandle.standardError ? stderr : stdout)
    }

    /// LogWarning prints a warning message to stderr
    public static func logWarning(_ format: String, _ args: CVarArg...) {
        logWarningTo(FileHandle.standardError, format, args)
    }

    /// LogWarningTo prints a warning message to the given file handle
    public static func logWarningTo(_ handle: FileHandle, _ format: String, _ args: [CVarArg]) {
        let msg = String(format: format, arguments: args)
        let output = styled("! \(msg)", .yellow)
        fputs(output + "\n", handle == FileHandle.standardError ? stderr : stdout)
    }

    /// LogError prints an error message to stderr
    public static func logError(_ format: String, _ args: CVarArg...) {
        logErrorTo(FileHandle.standardError, format, args)
    }

    /// LogErrorTo prints an error message to the given file handle
    public static func logErrorTo(_ handle: FileHandle, _ format: String, _ args: [CVarArg]) {
        let msg = String(format: format, arguments: args)
        let output = styled("✗ \(msg)", .red)
        fputs(output + "\n", handle == FileHandle.standardError ? stderr : stdout)
    }

    /// LogBullet prints a bulleted list item to stderr
    public static func logBullet(_ format: String, _ args: CVarArg...) {
        logBulletTo(FileHandle.standardError, format, args)
    }

    /// LogBulletTo prints a bulleted list item to the given file handle
    public static func logBulletTo(_ handle: FileHandle, _ format: String, _ args: [CVarArg]) {
        let msg = String(format: format, arguments: args)
        let bullet = styled("→", .pink)
        fputs("  \(bullet) \(msg)\n", handle == FileHandle.standardError ? stderr : stdout)
    }

    /// LogDim prints a dimmed message to stderr
    public static func logDim(_ format: String, _ args: CVarArg...) {
        logDimTo(FileHandle.standardError, format, args)
    }

    /// LogDimTo prints a dimmed message to the given file handle
    public static func logDimTo(_ handle: FileHandle, _ format: String, _ args: [CVarArg]) {
        let msg = String(format: format, arguments: args)
        let output = styled("  \(msg)", .gray)
        fputs(output + "\n", handle == FileHandle.standardError ? stderr : stdout)
    }

    /// Title returns a styled title
    public static func title(_ s: String) -> String {
        return "\(ANSIColor.bold.rawValue)\(ANSIColor.pink.rawValue)\(s)\(ANSIColor.reset.rawValue)"
    }

    /// Subtitle returns a styled subtitle
    public static func subtitle(_ s: String) -> String {
        return styled(s, .gray)
    }
}
