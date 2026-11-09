import { createSignal } from "solid-js";
import { render } from "solid-js/web";

// A Solid app hosts <chakra-chat> as a plain JSX element, with no wrapper.
// Its own copy of Solid is separate from the one inside the element bundle.
function App() {
  const [n, setN] = createSignal(0);
  return (
    <main>
      <h1>Solid host</h1>
      <button onClick={() => setN(n() + 1)}>rerender {n()}</button>
      <chakra-chat attr:server-url="http://127.0.0.1:8099" attr:profile="element" style={{ height: "420px" }}></chakra-chat>
    </main>
  );
}

render(() => <App />, document.getElementById("root"));
