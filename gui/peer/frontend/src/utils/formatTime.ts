/**
 * Formats a time input into a human-readable relative time string.
 *
 * @param timeInput - The time to format. Can be a Date object, ISO string, or timestamp number.
 * @returns A formatted string representing the relative time:
 *   - "Just now" for times less than 1 minute ago
 *   - "Xm ago" for times less than 1 hour ago
 *   - "Xh ago" for times less than 24 hours ago
 *   - Local date string for times 24+ hours ago
 *   - "N/A" if input is falsy
 *   - "Invalid date" if input cannot be parsed as a valid date
 *
 * @example
 * ```typescript
 * formatTime(new Date()) // "Just now"
 * formatTime(Date.now() - 30 * 60000) // "30m ago"
 * formatTime("2023-01-01") // "1/1/2023" (or local format)
 * ```
 */
export function formatTime(timeInput: Date | string | number): string {
    if (!timeInput) return 'N/A';
    const time = timeInput instanceof Date ? timeInput : new Date(timeInput);

    if (isNaN(time.getTime())) {
        return 'Invalid date';
    }

    const now = new Date();
    const diffMs = now.getTime() - time.getTime();
    const diffSeconds = Math.floor(diffMs / 1000);

    if (diffSeconds < 15) {
        return 'Just now';
    } else if (diffSeconds < 60) {
        return `${diffSeconds}s ago`;
    } else if (diffSeconds < 3600) {
        const diffMinutes = Math.floor(diffSeconds / 60);
        return `${diffMinutes}m ago`;
    } else if (diffSeconds < 86400) {
        const diffHours = Math.floor(diffSeconds / 3600);
        return `${diffHours}h ago`;
    }
    return time.toLocaleDateString();
}
