import { useState } from "react";
import { createRoot } from "react-dom/client";

// A React app hosts <chakra-chat> as a plain JSX element, with no wrapper.
// The counter re-renders the parent, and the element must keep its session.
function App() {
  const [n, setN] = useState(0);
  return (
    <main>
      <h1>React host</h1>
      <button onClick={() => setN(n + 1)}>rerender {n}</button>
      <chakra-chat server-url="http://127.0.0.1:8099" profile="element" style={{ height: "420px" }}></chakra-chat>
    </main>
  );
}

createRoot(document.getElementById("root")).render(<App />);
