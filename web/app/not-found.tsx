import type { Metadata } from "next";
import Link from "next/link";

import { buttonClass } from "./ui/button";
import { EmptyState } from "./ui/empty";

export const metadata: Metadata = { title: "Страница не найдена" };

// 404 по-русски — и для неизвестного адреса, и для notFound() страниц
// (урока нет). Вид — как у страниц без сессии: карточка под логотипом.
// «К урокам» без сессии уводит на вход.
export default function NotFound() {
  return (
    <main className="auth-page">
      <div className="card card-pad">
        <EmptyState
          icon="search"
          title="Страница не найдена"
          titleAs="h1"
          action={
            <Link href="/lessons" className={buttonClass({ variant: "primary" })}>
              К урокам
            </Link>
          }
        >
          Такой страницы нет: возможно, ссылка устарела или в адресе опечатка.
        </EmptyState>
      </div>
    </main>
  );
}
