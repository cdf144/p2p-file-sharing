/**
 * Formats a given number of bytes into a human-readable string with appropriate units (Bytes, KiB, MiB, GiB, TiB).
 *
 * @param bytes - The number of bytes to format.
 * @returns A string representing the formatted file size, e.g., "1.23 MiB".
 * The size is formatted to two decimal places.
 */
export function formatFileSize(bytes: number): string {
    if (bytes === 0) return '0 Bytes';
    const k = 1024;
    const sizes = ['Bytes', 'KiB', 'MiB', 'GiB', 'TiB'];
    // Logarithm base change rule: log_k(bytes) = ln(bytes) / ln(k)
    const i = Math.floor(Math.log(bytes) / Math.log(k));
    return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i];
}
