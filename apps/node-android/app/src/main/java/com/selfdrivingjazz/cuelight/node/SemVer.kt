package com.selfdrivingjazz.cuelight.node

/**
 * MAJOR.MINOR.PATCH[-PRERELEASE] version, parsed and ordered exactly like the
 * control's `internal/updates.ParseVersion`/`CompareVersions` (no build metadata).
 */
data class SemVer(val major: Int, val minor: Int, val patch: Int, val prerelease: String = "") : Comparable<SemVer> {
    override fun compareTo(other: SemVer): Int {
        compareValues(major, other.major).let { if (it != 0) return it }
        compareValues(minor, other.minor).let { if (it != 0) return it }
        compareValues(patch, other.patch).let { if (it != 0) return it }
        if (prerelease == other.prerelease) return 0
        if (prerelease.isEmpty()) return 1
        if (other.prerelease.isEmpty()) return -1
        val left = prerelease.split('.'); val right = other.prerelease.split('.')
        for (i in 0 until minOf(left.size, right.size)) {
            comparePrerelease(left[i], right[i]).let { if (it != 0) return it }
        }
        return compareValues(left.size, right.size)
    }

    override fun toString(): String = "$major.$minor.$patch" + if (prerelease.isEmpty()) "" else "-$prerelease"

    companion object {
        private val IDENTIFIER = Regex("[0-9A-Za-z-]+")

        fun parse(value: String): SemVer? {
            val dash = value.indexOf('-')
            val core = if (dash < 0) value else value.substring(0, dash)
            val prerelease = if (dash < 0) "" else value.substring(dash + 1)
            val parts = core.split('.')
            if (parts.size != 3) return null
            val numbers = parts.map { part ->
                if (part.isEmpty() || (part.length > 1 && part[0] == '0') || part.any { it !in '0'..'9' }) return null
                part.toIntOrNull() ?: return null
            }
            if (dash >= 0 && (prerelease.isEmpty() || prerelease.split('.').any { !IDENTIFIER.matches(it) })) return null
            return SemVer(numbers[0], numbers[1], numbers[2], prerelease)
        }

        /** Negative/zero/positive like the Go helper; null when either side does not parse. */
        fun compare(a: String, b: String): Int? {
            val left = parse(a) ?: return null
            val right = parse(b) ?: return null
            return left.compareTo(right)
        }

        private fun comparePrerelease(a: String, b: String): Int {
            val an = a.toIntOrNull(); val bn = b.toIntOrNull()
            return when {
                an != null && bn != null -> compareValues(an, bn)
                an != null -> -1
                bn != null -> 1
                else -> a.compareTo(b).coerceIn(-1, 1)
            }
        }
    }
}
