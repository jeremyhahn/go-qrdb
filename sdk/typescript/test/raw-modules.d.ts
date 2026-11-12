// Vite's `?raw` suffix imports a file's text as a string. The route parity
// test reads the SDK and server sources this way, since the SDK carries no
// Node type definitions.
declare module '*?raw' {
    const text: string;
    export default text;
}
