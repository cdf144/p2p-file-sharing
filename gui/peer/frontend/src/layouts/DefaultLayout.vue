<template>
    <header class="bg-gray-700 p-4 text-white">
        <h1 class="text-2xl font-bold">Peer File Sharing</h1>
    </header>
    <main class="container mx-auto space-y-6 p-4">
        <slot />
    </main>
    <footer class="bg-gray-800 p-4 text-center text-gray-400">
        &copy; {{ new Date().getFullYear() }} P2P File Sharing
    </footer>
</template>
